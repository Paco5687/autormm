package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
	"github.com/Paco5687/autormm/internal/selfupdate"
)

// updateRepo is the public GitHub repo the hub checks for new releases.
const updateRepo = "Paco5687/autormm"

// compareVersions orders two dotted numeric versions, reporting -1, 0 or +1,
// and ok=false when either side is not a plain dotted number.
//
// "Not a plain number" covers more than it sounds: "dev" from an unstamped
// build, a hand-built string, a release candidate. None of those can be
// ordered against a release, and guessing is how you end up moving in the
// wrong direction, so they are simply not comparable and no update is offered.
func compareVersions(a, b string) (int, bool) {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion splits "0.7.12" (or "v0.7.12") into its numeric components.
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// updateAvailable reports whether latest is a release this hub should move up
// to, and whether this hub is instead running something newer than anything
// published.
//
// It used to be `latest != Version`, which reads any difference as an upgrade.
// That is wrong in the one direction that destroys work: a hub built locally
// ahead of the public repo was offered a "update" that silently replaced it
// with an older release. Asking whether latest is *newer* cannot do that.
func updateAvailable(latest, current string) (available, ahead bool) {
	if latest == "" || current == "dev" {
		return false, false
	}
	cmp, ok := compareVersions(latest, current)
	if !ok {
		return false, false
	}
	return cmp > 0, cmp < 0
}

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
	available, ahead := updateAvailable(latest, Version)
	writeJSON(w, http.StatusOK, map[string]any{
		"current":   Version,
		"latest":    latest,
		"available": available,
		// Worth saying out loud rather than leaving as a silent "up to date":
		// a hub ahead of every published release is running something built by
		// hand, and the dashboard is the only place that would ever mention it.
		"ahead": ahead,
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
	if available, ahead := updateAvailable(latest, Version); !available {
		msg := "already up to date"
		if ahead {
			msg = "this hub is running " + Version + ", which is newer than the latest release (" +
				latest + "); refusing to replace it with an older build"
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": false, "message": msg})
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
