package server

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
)

// The audit trail.
//
// The hub already logged privileged acts — reboots, fleet actions, shell
// commands, logins — but only to its own stdout, which on a systemd user unit
// means "answerable by SSHing to the hub and grepping the journal". For a tool
// that grants complete control of every machine on the network, "who rebooted
// that box at 3am, and from where" has to be a question the dashboard answers.
//
// Records go to the history database when one is configured, and to the log
// either way, so a hub started without --db still behaves exactly as before.

// AuditEvent is one privileged action.
type AuditEvent struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"` // unix seconds
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Outcome string `json:"outcome,omitempty"` // ok | failed | denied
}

// auditLog persists audit events. A nil db keeps the logging behaviour and
// stores nothing, which is what a hub without history does.
type auditLog struct {
	mu sync.Mutex
	db *sql.DB
}

func newAuditLog(db *sql.DB) *auditLog {
	a := &auditLog{db: db}
	if db == nil {
		return a
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			ts      INTEGER NOT NULL,
			actor   TEXT NOT NULL,
			action  TEXT NOT NULL,
			target  TEXT,
			detail  TEXT,
			remote  TEXT,
			outcome TEXT
		);
		CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts DESC);`)
	if err != nil {
		log.Printf("audit: table unavailable, events will only be logged: %v", err)
		a.db = nil
	}
	return a
}

// record writes one event. Failures here are logged and swallowed: an audit
// write must never be the reason a reboot does not happen.
func (a *auditLog) record(e AuditEvent) {
	log.Printf("AUDIT %s actor=%s target=%s outcome=%s from=%s %s",
		e.Action, e.Actor, e.Target, e.Outcome, e.Remote, e.Detail)
	if a == nil || a.db == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.db.Exec(
		`INSERT INTO audit (ts, actor, action, target, detail, remote, outcome) VALUES (?,?,?,?,?,?,?)`,
		e.TS, e.Actor, e.Action, e.Target, e.Detail, e.Remote, e.Outcome)
	if err != nil {
		log.Printf("audit: write failed: %v", err)
	}
}

// query returns the most recent events, newest first.
func (a *auditLog) query(limit int, actionLike string) ([]AuditEvent, error) {
	if a == nil || a.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id, ts, actor, action, COALESCE(target,''), COALESCE(detail,''),
	             COALESCE(remote,''), COALESCE(outcome,'') FROM audit`
	args := []any{}
	if actionLike != "" {
		q += ` WHERE action LIKE ?`
		args = append(args, "%"+actionLike+"%")
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)

	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Target,
			&e.Detail, &e.Remote, &e.Outcome); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// audit records an action performed through an authenticated request.
func (s *Server) audit(r *http.Request, action, target, detail, outcome string) {
	s.auditLog.record(AuditEvent{
		TS:      time.Now().Unix(),
		Actor:   actorOf(r, s),
		Action:  action,
		Target:  target,
		Detail:  truncateForLog(detail),
		Remote:  clientIP(r),
		Outcome: outcome,
	})
}

// actorOf names whoever made this request.
//
// A login session carries the username in its ticket subject; the standing
// admin token does not identify a person at all, and saying so plainly is more
// honest than attributing its actions to nobody.
func actorOf(r *http.Request, s *Server) string {
	tok := bearer(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if t, err := auth.VerifyTicket(s.secret, tok); err == nil {
		if name := strings.TrimPrefix(t.Session, loginSubjectPrefix); name != t.Session {
			return name
		}
	}
	return "admin token"
}

// clientIP is the caller's address without the ephemeral port, which is noise.
func clientIP(r *http.Request) string {
	// A reverse proxy in front of the hub is the normal deployment, so prefer
	// the forwarded address when one is present — otherwise every event is
	// attributed to the proxy and the field is worthless.
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if i := strings.IndexByte(f, ','); i >= 0 {
			f = f[:i]
		}
		return strings.TrimSpace(f)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 && !strings.HasSuffix(addr, "]") {
		return addr[:i]
	}
	return addr
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.auditLog == nil || s.auditLog.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []AuditEvent{},
			"note":   "start the hub with --db to keep an audit history",
		})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.auditLog.query(limit, r.URL.Query().Get("action"))
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
