package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
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
	// Services to watch. An alert is raised when one is not running.
	Services []string `json:"services,omitempty"`
	// Remediate maps a rule name ("service:docker", "disk") to the id of a
	// script to run when it fires, before deciding whether to alert.
	Remediate map[string]string `json:"remediate,omitempty"`
}

// isZero reports whether this pref carries no settings at all.
//
// Spelled out field by field because HostPref now contains a slice and can no
// longer be compared with ==. A field added here and forgotten below means an
// empty-looking entry is kept rather than cleaned up, which is harmless; the
// reverse — dropping an entry that holds a real setting — is not, so new
// fields belong in this list.
func (p HostPref) isZero() bool {
	return p.CPU == 0 && p.Mem == 0 && p.Disk == 0 &&
		!p.Mute && p.MuteUntil.IsZero() && p.Note == "" &&
		len(p.Services) == 0 && len(p.Remediate) == 0
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

// resolve returns the thresholds in force for a host, layering policies under
// its own settings.
//
// A key in this store is either an agent id or a selector — "all", "os:linux",
// "tag:server" — which is what turns per-machine settings into policy. Layers
// are applied least to most specific and merged field by field, so a policy can
// set a disk threshold for every server while one machine still carries its own
// CPU number. Matching reuses the same selector logic as script targeting, so a
// tag written "Prod, Linux" behaves identically in both places.
//
// Mute is deliberately NOT inherited. Silencing is an incident-time act on a
// named machine, and a policy muting a whole tag would need a way to say
// "except this one", which zero-means-inherit cannot express.
func (h *hostPrefs) resolve(v protocol.HostView) HostPref {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Sorted, because two tag policies can match one host and map iteration
	// order would otherwise decide which threshold won on any given tick.
	var keys []string
	for key := range h.m {
		if IsSelector(key) && matchesTarget(key, v) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if a, b := policyRank(keys[i]), policyRank(keys[j]); a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})

	out := HostPref{}
	apply := func(key string) {
		p, ok := h.m[key]
		if !ok {
			return
		}
		if p.CPU > 0 {
			out.CPU = p.CPU
		}
		if p.Mem > 0 {
			out.Mem = p.Mem
		}
		if p.Disk > 0 {
			out.Disk = p.Disk
		}
	}
	for _, k := range keys {
		apply(k)
	}
	apply(v.AgentID) // the host's own overrides win over every policy

	// Silencing and the note belong to the machine, never to a policy.
	if own, ok := h.m[v.AgentID]; ok {
		out.Mute, out.MuteUntil, out.Note = own.Mute, own.MuteUntil, own.Note
	}
	return out
}

// resolveServices returns every service watched on a host: the union of its own
// list and any policy covering it.
//
// Union rather than override, which is the opposite of how thresholds resolve.
// A number has one right answer, so the narrowest wins; a watch list is a set of
// independent wants, and a host adding Plex to its own list should not stop the
// policy watching Docker on every server.
func (h *hostPrefs) resolveServices(v protocol.HostView) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := map[string]bool{}
	var out []string
	add := func(names []string) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" && !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				out = append(out, n)
			}
		}
	}
	var keys []string
	for key := range h.m {
		if IsSelector(key) && matchesTarget(key, v) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys) // stable order, so the list does not reshuffle each poll
	for _, k := range keys {
		add(h.m[k].Services)
	}
	add(h.m[v.AgentID].Services)
	return out
}

// policyRank orders selectors from broadest to narrowest.
func policyRank(key string) int {
	switch {
	case key == targetAll:
		return 0
	case strings.HasPrefix(key, osPrefix):
		return 1
	default: // tag:
		return 2
	}
}

// IsPolicyKey reports whether a prefs key describes a set of hosts rather than
// one machine.
func IsPolicyKey(key string) bool { return IsSelector(key) }

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
	if p.isZero() {
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

// forget drops a host's thresholds and mute window.
//
// Left behind, they would be waiting to be silently reapplied to whatever next
// took the same agent id — and a machine inheriting another's alert thresholds
// is a bad way to find out this file was never cleaned up.
func (h *hostPrefs) forget(agentID string) error {
	return h.set(agentID, HostPref{})
}
