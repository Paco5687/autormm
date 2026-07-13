package server

import (
	"database/sql"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
)

// Script is a stored, reusable command/script.
type Script struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Shell   string `json:"shell"`
	Content string `json:"content"`
	Created int64  `json:"created"`
}

// Schedule runs a script on a host on a cron schedule.
type Schedule struct {
	ID       string `json:"id"`
	ScriptID string `json:"script_id"`
	AgentID  string `json:"agent_id"`
	Cron     string `json:"cron"`
	Enabled  bool   `json:"enabled"`
	LastRun  int64  `json:"last_run"`
}

// Run is one recorded execution of a script.
type Run struct {
	ID         string `json:"id"`
	ScriptID   string `json:"script_id"`
	ScriptName string `json:"script_name"`
	AgentID    string `json:"agent_id"`
	Started    int64  `json:"started"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Error      string `json:"error"`
	Source     string `json:"source"` // manual | schedule
}

// ScriptStore persists scripts, schedules and run history. It shares the
// History database handle.
type ScriptStore struct {
	mu sync.Mutex
	db *sql.DB
}

// NewScriptStore initialises the schema on db. Returns nil if db is nil.
func NewScriptStore(db *sql.DB) (*ScriptStore, error) {
	if db == nil {
		return nil, nil
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scripts (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, shell TEXT, content TEXT NOT NULL, created INTEGER);
		CREATE TABLE IF NOT EXISTS schedules (
			id TEXT PRIMARY KEY, script_id TEXT NOT NULL, agent_id TEXT NOT NULL,
			cron TEXT NOT NULL, enabled INTEGER, last_run INTEGER);
		CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY, script_id TEXT, script_name TEXT, agent_id TEXT,
			started INTEGER, exit_code INTEGER, stdout TEXT, stderr TEXT, error TEXT, source TEXT);
		CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started);
	`); err != nil {
		return nil, err
	}
	return &ScriptStore{db: db}, nil
}

// SaveScript inserts or updates a script (id assigned if empty) and returns it.
func (s *ScriptStore) SaveScript(sc Script) (*Script, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc.ID == "" {
		sc.ID = auth.RandomID(8)
		sc.Created = time.Now().Unix()
		_, err := s.db.Exec(`INSERT INTO scripts (id,name,shell,content,created) VALUES (?,?,?,?,?)`,
			sc.ID, sc.Name, sc.Shell, sc.Content, sc.Created)
		return &sc, err
	}
	_, err := s.db.Exec(`UPDATE scripts SET name=?, shell=?, content=? WHERE id=?`,
		sc.Name, sc.Shell, sc.Content, sc.ID)
	return &sc, err
}

func (s *ScriptStore) GetScript(id string) (*Script, error) {
	var sc Script
	err := s.db.QueryRow(`SELECT id,name,shell,content,created FROM scripts WHERE id=?`, id).
		Scan(&sc.ID, &sc.Name, &sc.Shell, &sc.Content, &sc.Created)
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *ScriptStore) ListScripts() ([]Script, error) {
	rows, err := s.db.Query(`SELECT id,name,shell,content,created FROM scripts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Shell, &sc.Content, &sc.Created); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *ScriptStore) DeleteScript(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM scripts WHERE id=?`, id)
	if err == nil {
		s.db.Exec(`DELETE FROM schedules WHERE script_id=?`, id)
	}
	return err
}

// SaveSchedule inserts or updates a schedule.
func (s *ScriptStore) SaveSchedule(sch Schedule) (*Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sch.ID == "" {
		sch.ID = auth.RandomID(8)
		_, err := s.db.Exec(`INSERT INTO schedules (id,script_id,agent_id,cron,enabled,last_run) VALUES (?,?,?,?,?,?)`,
			sch.ID, sch.ScriptID, sch.AgentID, sch.Cron, b2i(sch.Enabled), sch.LastRun)
		return &sch, err
	}
	_, err := s.db.Exec(`UPDATE schedules SET script_id=?,agent_id=?,cron=?,enabled=? WHERE id=?`,
		sch.ScriptID, sch.AgentID, sch.Cron, b2i(sch.Enabled), sch.ID)
	return &sch, err
}

func (s *ScriptStore) ListSchedules() ([]Schedule, error) {
	rows, err := s.db.Query(`SELECT id,script_id,agent_id,cron,enabled,last_run FROM schedules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sch Schedule
		var en int
		if err := rows.Scan(&sch.ID, &sch.ScriptID, &sch.AgentID, &sch.Cron, &en, &sch.LastRun); err != nil {
			return nil, err
		}
		sch.Enabled = en != 0
		out = append(out, sch)
	}
	return out, rows.Err()
}

func (s *ScriptStore) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM schedules WHERE id=?`, id)
	return err
}

func (s *ScriptStore) markScheduleRun(id string, when int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`UPDATE schedules SET last_run=? WHERE id=?`, when, id)
}

// SaveRun records a run (capping stored output) and returns it.
func (s *ScriptStore) SaveRun(r Run) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = auth.RandomID(10)
	r.Stdout = capStr(r.Stdout, 64*1024)
	r.Stderr = capStr(r.Stderr, 64*1024)
	s.db.Exec(`INSERT INTO runs (id,script_id,script_name,agent_id,started,exit_code,stdout,stderr,error,source)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ScriptID, r.ScriptName, r.AgentID, r.Started, r.ExitCode, r.Stdout, r.Stderr, r.Error, r.Source)
	return &r
}

// ListRuns returns recent runs, optionally filtered by agent, newest first.
func (s *ScriptStore) ListRuns(agentID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id,script_id,script_name,agent_id,started,exit_code,stdout,stderr,error,source FROM runs`
	args := []any{}
	if agentID != "" {
		q += ` WHERE agent_id=?`
		args = append(args, agentID)
	}
	q += ` ORDER BY started DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.ScriptID, &r.ScriptName, &r.AgentID, &r.Started,
			&r.ExitCode, &r.Stdout, &r.Stderr, &r.Error, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func capStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n[truncated]"
	}
	return s
}
