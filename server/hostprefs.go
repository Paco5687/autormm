package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HostPref is per-host alerting configuration: threshold overrides and a
// maintenance window.
//
// Global thresholds cannot fit a mixed fleet. A Proxmox box legitimately sitting
// at 90% memory pages forever, and the only remedy without this was to raise the
// global threshold until it stopped — which silences every other host too. A
// zero here means "use the global setting", so a host with no overrides costs
// nothing.
type HostPref struct {
	CPU       float64   `json:"cpu,omitempty"`  // 0 = inherit the global threshold
	Mem       float64   `json:"mem,omitempty"`  //
	Disk      float64   `json:"disk,omitempty"` //
	Mute      bool      `json:"mute,omitempty"` // silence indefinitely
	MuteUntil time.Time `json:"mute_until,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// Silenced reports whether alerts for this host should be suppressed now.
func (p HostPref) Silenced(now time.Time) bool {
	return p.Mute || (!p.MuteUntil.IsZero() && now.Before(p.MuteUntil))
}

// hostPrefs persists per-host alert settings beside the rest of the hub's
// state. Small, rarely written, read on every evaluation — so it is held in
// memory and written through on change.
type hostPrefs struct {
	mu   sync.RWMutex
	path string
	m    map[string]HostPref
}

func newHostPrefs(dir string) *hostPrefs {
	p := &hostPrefs{m: map[string]HostPref{}}
	if dir != "" {
		p.path = filepath.Join(dir, "hostprefs.json")
		if b, err := os.ReadFile(p.path); err == nil {
			_ = json.Unmarshal(b, &p.m)
		}
	}
	return p
}

func (h *hostPrefs) get(agentID string) HostPref {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m[agentID]
}

func (h *hostPrefs) all() map[string]HostPref {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]HostPref, len(h.m))
	for k, v := range h.m {
		out[k] = v
	}
	return out
}

func (h *hostPrefs) set(agentID string, p HostPref) error {
	h.mu.Lock()
	if p == (HostPref{}) {
		delete(h.m, agentID) // an all-defaults entry is just clutter
	} else {
		h.m[agentID] = p
	}
	b, _ := json.MarshalIndent(h.m, "", "  ")
	path := h.path
	h.mu.Unlock()
	if path == "" {
		return nil
	}
	return os.WriteFile(path, b, 0o600)
}

// handleHostPrefs reads or writes one host's alert overrides.
func (s *Server) handleHostPrefs(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.prefs.all())
	case http.MethodPost:
		var req struct {
			AgentID   string   `json:"agent_id"`
			Pref      HostPref `json:"pref"`
			MuteHours float64  `json:"mute_hours,omitempty"` // convenience: silence for a while
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.AgentID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.MuteHours > 0 {
			req.Pref.MuteUntil = time.Now().Add(time.Duration(req.MuteHours * float64(time.Hour)))
		}
		if err := s.prefs.set(req.AgentID, req.Pref); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pref": s.prefs.get(req.AgentID)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
