// Package server implements the autormm hub: it accepts persistent agent
// control connections, serves the RMM dashboard + REST API to clients, and
// relays remote-desktop media sockets between clients and agents.
package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
)

// Config holds server settings (populated from flags/env in cmd/autormm-server).
type Config struct {
	Addr         string        // listen address, e.g. ":8765"
	AdminToken   string        // bearer token for clients / dashboard
	EnrollToken  string        // shared secret agents present to register
	SecretPhrase string        // HMAC base for session tickets
	OfflineAfter time.Duration // grace period before a quiet host is flagged stale
	HistoryLen   int           // samples kept per host for sparklines
	DBPath       string        // SQLite path for persisted history; empty => disabled
	Retention    time.Duration // how long to keep persisted samples
	Alerts       AlertConfig   // thresholds + notification sinks
	TLSCert      string        // optional; empty => plain HTTP (e.g. behind Traefik)
	TLSKey       string
}

// Server is the running hub.
type Server struct {
	cfg      Config
	secret   []byte
	store    *Store
	sessions *sessionRegistry
	execReg  *execRegistry
	invReg   *invRegistry
	history  *History
	scripts  *ScriptStore
	alerter  *Alerter
	httpSrv  *http.Server
}

// New builds a Server from cfg.
func New(cfg Config) *Server {
	if cfg.HistoryLen <= 0 {
		cfg.HistoryLen = 60
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = 30 * time.Second
	}
	var hist *History
	if cfg.DBPath != "" {
		h, err := OpenHistory(cfg.DBPath, cfg.Retention)
		if err != nil {
			log.Printf("history disabled: could not open %s: %v", cfg.DBPath, err)
		} else {
			hist = h
			log.Printf("history enabled: %s (retention %s)", cfg.DBPath, h.retention)
		}
	}
	var scripts *ScriptStore
	if hist != nil {
		if ss, err := NewScriptStore(hist.DB()); err != nil {
			log.Printf("scripts disabled: %v", err)
		} else {
			scripts = ss
		}
	}
	s := &Server{
		cfg:      cfg,
		secret:   auth.DeriveSecret(cfg.SecretPhrase),
		store:    NewStore(cfg.HistoryLen, cfg.OfflineAfter, hist),
		sessions: newSessionRegistry(),
		execReg:  newExecRegistry(),
		invReg:   newInvRegistry(),
		history:  hist,
		scripts:  scripts,
		alerter:  NewAlerter(cfg.Alerts),
	}
	return s
}

// Run starts serving until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.httpSrv = &http.Server{Addr: s.cfg.Addr, Handler: logRequests(s.Handler())}

	go s.store.reaper(ctx)
	go s.pruneLoop(ctx)
	go s.alerter.Run(ctx, s.store)
	go s.schedulerLoop(ctx)

	errCh := make(chan error, 1)
	go func() {
		if s.cfg.TLSCert != "" && s.cfg.TLSKey != "" {
			log.Printf("autormm-server listening on %s (TLS)", s.cfg.Addr)
			errCh <- s.httpSrv.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
		} else {
			log.Printf("autormm-server listening on %s (http)", s.cfg.Addr)
			errCh <- s.httpSrv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.httpSrv.Shutdown(shutCtx)
		s.history.Close()
		return err
	case err := <-errCh:
		return err
	}
}

// pruneLoop periodically trims persisted history beyond the retention window.
func (s *Server) pruneLoop(ctx context.Context) {
	if s.history == nil {
		return
	}
	s.history.prune()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.history.prune()
		}
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep media/agent sockets quiet; log only ordinary requests.
		if r.URL.Path != "/agent/ws" && r.URL.Path != "/agent/session" && r.URL.Path != "/client/session" {
			log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
