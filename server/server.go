// Package server implements the autormm hub: it accepts persistent agent
// control connections, serves the RMM dashboard + REST API to clients, and
// relays remote-desktop media sockets between clients and agents.
package server

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/adminstore"
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
	AdminStore   string // path to admins.json for username/password login
	FFmpegURL    string // where hosts fetch ffmpeg for H.264; empty => DefaultFFmpegURL
	// SyslogAddr turns on the syslog listener, e.g. ":514". Empty means off.
	// UDP syslog is unauthenticated, so this belongs on a LAN address.
	SyslogAddr string
}

// Server is the running hub.
type Server struct {
	cfg        Config
	secret     []byte
	store      *Store
	svc        *svcWatcher
	remediator *remediator
	sessions   *sessionRegistry
	execReg    *execRegistry
	invReg     *invRegistry
	history    *History
	auditLog   *auditLog
	scripts    *ScriptStore
	alerter    *Alerter
	prefs      *hostPrefs
	netChecks  *netChecks
	syslog     *syslogStore
	netSeenMu  sync.Mutex
	netSeen    map[string]bool
	admins     *adminstore.Store
	httpSrv    *http.Server
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
	// Shares the history database when there is one; without it the audit log
	// still logs, exactly as the hub did before it existed.
	var auditDB *sql.DB
	if hist != nil {
		auditDB = hist.DB()
	}

	s := &Server{
		cfg:        cfg,
		secret:     auth.DeriveSecret(cfg.SecretPhrase),
		store:      NewStore(cfg.HistoryLen, cfg.OfflineAfter, hist),
		svc:        newSvcWatcher(),
		remediator: newRemediator(),
		sessions:   newSessionRegistry(),
		execReg:    newExecRegistry(),
		invReg:     newInvRegistry(),
		history:    hist,
		auditLog:   newAuditLog(auditDB),
		scripts:    scripts,
		alerter:    NewAlerter(cfg.Alerts),
		// Per-host alert overrides live beside the admin store, which is the
		// hub's existing home for small persisted state.
		prefs: newHostPrefs(filepath.Dir(cfg.AdminStore)),
		// Agentless checks live beside the other small persisted state.
		netChecks: newNetChecks(filepath.Dir(cfg.AdminStore)),
	}
	s.alerter.prefs = s.prefs
	if cfg.AdminStore != "" {
		s.admins = adminstore.New(cfg.AdminStore)
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
	// The alerter reads watched-service state through the hub rather than
	// holding it, so an Alerter built directly in a test raises no service
	// rules and behaves exactly as it did before this existed.
	s.alerter.svcStates = s.svc.states
	s.alerter.watched = s.watchedServicesFor
	s.alerter.remediate = s.tryRemediate

	go s.netCheckLoop(ctx)
	if s.cfg.SyslogAddr != "" {
		s.syslog = newSyslogStore()
		go s.runSyslog(ctx, s.cfg.SyslogAddr)
	}
	go s.svcWatchLoop(ctx)

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
