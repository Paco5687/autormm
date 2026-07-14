package server

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
	"github.com/Paco5687/autormm/internal/selfupdate"
)

// updateRepo is the public GitHub repo the hub checks for new releases.
const updateRepo = "Paco5687/autormm"

// handleUpdateCheck reports the hub's current version and the latest release.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	latest, err := selfupdate.GitHubLatest(updateRepo)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"current": Version, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current":   Version,
		"latest":    latest,
		"available": latest != "" && latest != Version && Version != "dev",
	})
}

// handleUpdateApply downloads the latest hub binary, validates it, swaps it in,
// and restarts (systemd Restart=always brings it back on the new version).
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	latest, err := selfupdate.GitHubLatest(updateRepo)
	if err != nil {
		http.Error(w, "could not check for updates: "+err.Error(), http.StatusBadGateway)
		return
	}
	if latest == "" || latest == Version {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": false, "message": "already up to date"})
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp, err := selfupdate.FetchGitHubServer(updateRepo, latest, filepath.Dir(exe))
	if err != nil {
		http.Error(w, "download failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := selfupdate.Validate(tmp, latest); err != nil {
		os.Remove(tmp)
		http.Error(w, "update failed validation: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Respond before restarting so the client sees the result.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": true, "version": latest,
		"message": "hub is updating to " + latest + " and will restart"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(time.Second)
		if _, err := selfupdate.ReplaceRunningBinary(tmp); err != nil {
			return
		}
		os.Exit(0) // systemd Restart=always relaunches the new binary
	}()
}

// handleUpdatePush tells every online agent to check the hub for an update now.
func (s *Server) handleUpdatePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conns := s.store.onlineConns()
	msg := map[string]string{"type": protocol.TypeUpdateNow}
	for _, c := range conns {
		c.sendJSON(msg)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notified": len(conns)})
}
