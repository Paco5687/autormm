package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/auth"
	"github.com/Paco5687/autormm/internal/protocol"
)

//go:embed web
var webFS embed.FS

// Version is set at release time via -ldflags -X (see .goreleaser.yaml). Agents
// query /version and self-update to match it.
var Version = "dev"

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Homelab deployment sits behind Traefik / a trusted LAN; allow any origin.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler builds the HTTP handler (used by Run and by tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

func (s *Server) routes(mux *http.ServeMux) {
	sub, _ := fs.Sub(webFS, "web")

	mux.HandleFunc("/", s.handleIndex(sub))
	mux.HandleFunc("/viewer", s.handlePage(sub, "viewer.html"))
	mux.HandleFunc("/terminal", s.handlePage(sub, "terminal.html"))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// The version this hub was built at; agents self-update to match it.
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": Version})
	})

	// One-command host enrollment.
	mux.HandleFunc("/install.sh", s.handleInstallScript)
	mux.HandleFunc("/install.ps1", s.handleInstallPS1)
	mux.HandleFunc("/install-elevated.ps1", s.handleInstallElevatedPS1)
	mux.HandleFunc("/download/agent", s.handleDownloadAgent)
	mux.HandleFunc("/api/enroll", s.handleEnroll)

	mux.HandleFunc("/api/hosts", s.handleHosts)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/alerts", s.handleAlerts)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/inventory", s.handleInventory)
	mux.HandleFunc("/api/scripts", s.handleScripts)
	mux.HandleFunc("/api/scripts/run", s.handleScriptRun)
	mux.HandleFunc("/api/schedules", s.handleSchedules)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/session", s.handleCreateSession)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/patch/status", s.handlePatchStatus)
	mux.HandleFunc("/api/patch/install", s.handlePatchInstall)
	mux.HandleFunc("/api/patch/reboot", s.handlePatchReboot)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/authinfo", s.handleAuthInfo)
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/admin/accounts", s.handleAdminAccounts)
	mux.HandleFunc("/api/admin/set", s.handleAdminSet)
	mux.HandleFunc("/api/admin/remove", s.handleAdminRemove)
	mux.HandleFunc("/api/update/check", s.handleUpdateCheck)
	mux.HandleFunc("/api/update/apply", s.handleUpdateApply)
	mux.HandleFunc("/api/update/push", s.handleUpdatePush)

	mux.HandleFunc("/agent/ws", s.handleAgentControl)
	mux.HandleFunc("/agent/session", s.handleAgentSession)
	mux.HandleFunc("/client/session", s.handleClientSession)
}

func (s *Server) handleIndex(sub fs.FS) http.HandlerFunc {
	page := s.handlePage(sub, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page(w, r)
	}
}

func (s *Server) handlePage(sub fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
}

// ---- auth ----

func (s *Server) checkAdmin(r *http.Request) bool {
	tok := bearer(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if auth.TokenEqual(tok, s.cfg.AdminToken) {
		return true
	}
	// A login-session token (issued by /api/login) also authorises admin access.
	if t, err := auth.VerifyTicket(s.secret, tok); err == nil && strings.HasPrefix(t.Session, loginSubjectPrefix) {
		return true
	}
	return false
}

const loginSubjectPrefix = "login:"

// loginSessionTTL is how long a password login stays valid.
const loginSessionTTL = 12 * time.Hour

// handleLogin verifies a username/password against the admin store and returns a
// signed session token the dashboard uses as its bearer.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.admins == nil {
		http.Error(w, "password login is not configured", http.StatusNotImplemented)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.admins.Verify(req.Username, req.Password) {
		log.Printf("AUDIT login failed user=%q from=%s", req.Username, r.RemoteAddr)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	log.Printf("AUDIT login ok user=%q from=%s", req.Username, r.RemoteAddr)
	tok := auth.SignTicket(s.secret, loginSubjectPrefix+req.Username, "", loginSessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":   tok,
		"expires": time.Now().Add(loginSessionTTL).Unix(),
	})
}

// handleAuthInfo tells the dashboard which login methods are available: whether
// any admin accounts exist (password login) and whether first-run setup is
// needed (no accounts yet).
func (s *Server) handleAuthInfo(w http.ResponseWriter, r *http.Request) {
	hasAccounts := false
	canSetup := false
	if s.admins != nil {
		canSetup = true
		if names, err := s.admins.List(); err == nil && len(names) > 0 {
			hasAccounts = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"password_login": hasAccounts,
		"needs_setup":    canSetup && !hasAccounts,
	})
}

// handleSetup creates the first admin account on a fresh hub (no auth required,
// but only while zero accounts exist — trust on first use). Returns a login
// token so the browser is signed in immediately.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.admins == nil {
		http.Error(w, "account storage unavailable", http.StatusNotImplemented)
		return
	}
	if names, _ := s.admins.List(); len(names) > 0 {
		http.Error(w, "setup already complete — sign in instead", http.StatusConflict)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.admins.Set(req.Username, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("AUDIT setup: created first admin %q from=%s", req.Username, r.RemoteAddr)
	tok := auth.SignTicket(s.secret, loginSubjectPrefix+req.Username, "", loginSessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}

// ---- REST ----

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, s.store.views())
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID     string `json:"agent_id"`
		Command     string `json:"command"`
		Shell       string `json:"shell"`
		TimeoutSecs int    `json:"timeout_secs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AgentID == "" || req.Command == "" {
		http.Error(w, "agent_id and command are required", http.StatusBadRequest)
		return
	}
	res, err := s.runOnAgent(req.AgentID, req.Command, req.Shell, req.TimeoutSecs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exit_code": res.ExitCode,
		"stdout":    res.Stdout,
		"stderr":    res.Stderr,
		"error":     res.Err,
		"truncated": res.Truncated,
	})
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		http.Error(w, "missing agent", http.StatusBadRequest)
		return
	}
	conn := s.store.connFor(agent)
	if conn == nil {
		http.Error(w, "host offline", http.StatusConflict)
		return
	}
	reqID := auth.RandomID(12)
	ch := s.invReg.create(reqID)
	defer s.invReg.remove(reqID)
	conn.sendJSON(protocol.InventoryRequest{Type: protocol.TypeInventory, ReqID: reqID})

	select {
	case resp := <-ch:
		writeJSON(w, http.StatusOK, map[string]any{
			"source":   resp.Source,
			"packages": resp.Packages,
			"count":    len(resp.Packages),
			"error":    resp.Err,
		})
	case <-time.After(40 * time.Second):
		http.Error(w, "timed out waiting for agent", http.StatusGatewayTimeout)
	}
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, s.alerter.Active())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		http.Error(w, "missing agent", http.StatusBadRequest)
		return
	}
	dur := parseRange(r.URL.Query().Get("range"), time.Hour)
	to := time.Now()
	pts, err := s.history.Query(agent, to.Add(-dur), to, 120)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.history != nil,
		"points":  pts,
	})
}

// parseRange accepts Go durations plus a "<n>d" days suffix.
func parseRange(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req protocol.SessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = protocol.SessionScreen
	}
	switch kind {
	case protocol.SessionTerminal, protocol.SessionFile:
		if !s.store.canExec(req.AgentID) {
			http.Error(w, "host offline or shell/file access disabled", http.StatusConflict)
			return
		}
	default:
		if !s.store.canStream(req.AgentID) {
			http.Error(w, "host offline or screen streaming unavailable", http.StatusConflict)
			return
		}
	}
	fps := req.FPS
	if fps <= 0 || fps > 30 {
		fps = 10
	}
	q := req.Quality
	if q <= 0 || q > 100 {
		q = 60
	}
	sid := auth.RandomID(18)
	s.sessions.create(sid, req.AgentID, kind, fps, q)
	ticket := auth.SignTicket(s.secret, sid, req.AgentID, 60*time.Second)
	writeJSON(w, http.StatusOK, protocol.SessionResponse{
		Session: sid,
		Token:   ticket,
		WSPath:  "/client/session",
	})
}

// ---- agent control ----

func (s *Server) handleAgentControl(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if !auth.TokenEqual(tok, s.cfg.EnrollToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// First message must be a registration.
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return
	}
	var reg protocol.Register
	if err := json.Unmarshal(data, &reg); err != nil || reg.Type != protocol.TypeRegister || reg.AgentID == "" {
		ws.Close()
		return
	}

	conn := newAgentConn(reg.AgentID, ws, s.execReg, s.invReg)
	if old := s.store.register(reg, conn); old != nil {
		old.close()
	}
	log.Printf("agent registered: %s (%s, %s) stream=%v", reg.AgentID, reg.Hostname, reg.Platform, reg.CanStream)

	go conn.writePump()
	conn.readLoop(s.store) // blocks until the connection drops
	log.Printf("agent disconnected: %s", reg.AgentID)
}

// ---- media sockets ----

func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	tkt, err := auth.VerifyTicket(s.secret, r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess := s.sessions.get(tkt.Session)
	if sess == nil || sess.agentID != tkt.AgentID {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if !sess.deliverAgent(ws) {
		ws.Close() // viewer gone or agent already attached
	}
	// Connection is handed to the viewer's relay; do not close it here.
}

func (s *Server) handleClientSession(w http.ResponseWriter, r *http.Request) {
	tkt, err := auth.VerifyTicket(s.secret, r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess := s.sessions.get(tkt.Session)
	if sess == nil || sess.agentID != tkt.AgentID {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	defer s.sessions.remove(sess.id)

	clientWS, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer clientWS.Close()

	agentControl := s.store.connFor(sess.agentID)
	if agentControl == nil {
		writeWSError(clientWS, "host is offline")
		return
	}

	// Negotiate the video codec: intersect the viewer's decoder caps (sent as a
	// ?caps= query param) with the agent's encoder caps. Falls back to JPEG-tile.
	codec := ""
	if sess.kind == protocol.SessionScreen {
		var clientCaps []string
		if c := r.URL.Query().Get("caps"); c != "" {
			clientCaps = strings.Split(c, ",")
		}
		codec = pickCodec(clientCaps, s.store.encoderCaps(sess.agentID))
	}

	// Ask the agent to open its media socket for this session.
	mediaTicket := auth.SignTicket(s.secret, sess.id, sess.agentID, 30*time.Second)
	agentControl.sendJSON(protocol.StartSession{
		Type:    protocol.TypeStartSession,
		Session: sess.id,
		Token:   mediaTicket,
		Kind:    sess.kind,
		Codec:   codec,
		FPS:     sess.fps,
		Quality: sess.quality,
	})

	select {
	case agentWS := <-sess.agentCh:
		relay(clientWS, agentWS)
	case <-time.After(12 * time.Second):
		writeWSError(clientWS, "agent did not connect (screen capture unavailable?)")
	}
}

func writeWSError(ws *websocket.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"t": "error", "message": msg})
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	ws.WriteMessage(websocket.TextMessage, b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
