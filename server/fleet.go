package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Fleet actions: one operation applied to every host a selector matches.
//
// Targeting already existed for scripts, but reboot and patching were reachable
// only one agent id at a time, which is precisely the shape of work that made
// somebody click through the whole fleet by hand. This runs the same operations
// against "all", "tag:server" or "os:windows" and reports what each host did —
// per host, because a fleet action that returns a single verdict hides the two
// machines that failed.

// fleetResult is one host's outcome.
type fleetResult struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname"`
	OK       bool   `json:"ok"`
	Output   string `json:"output,omitempty"`
}

func (s *Server) handleFleetAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Target string `json:"target"` // agent id, or all / tag:… / os:…
		Action string `json:"action"` // reboot | patch-check | patch-install
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Target == "" || req.Action == "" {
		http.Error(w, "target and action are required", http.StatusBadRequest)
		return
	}

	views := s.store.views()
	targets := resolveTarget(req.Target, views)
	if len(targets) == 0 {
		http.Error(w, "no online hosts match "+req.Target, http.StatusConflict)
		return
	}
	names := map[string]string{}
	for _, v := range views {
		names[v.AgentID] = v.Hostname
	}

	s.audit(r, "fleet:"+req.Action, req.Target,
		fmt.Sprintf("%d host(s)", len(targets)), "ok")

	// Concurrently: these wait on remote machines, and a dozen hosts in series
	// would take long enough that the request itself becomes the problem.
	results := make([]fleetResult, len(targets))
	var wg sync.WaitGroup
	for i, id := range targets {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			ok, out := s.fleetOne(req.Action, id)
			results[i] = fleetResult{AgentID: id, Hostname: names[id], OK: ok, Output: out}
		}(i, id)
	}
	wg.Wait()

	// Failures first: with twenty hosts, the ones that did not do what was
	// asked are the entire message.
	sort.SliceStable(results, func(i, j int) bool { return !results[i].OK && results[j].OK })

	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"total":   len(results),
		"failed":  failed,
	})
}

// fleetOne performs one action on one host.
func (s *Server) fleetOne(action, agentID string) (bool, string) {
	switch action {
	case "reboot":
		shell, cmd, ok := rebootCommand(s.store.osFor(agentID))
		if !ok {
			return false, "reboot is not supported on this host"
		}
		return s.runReport(agentID, cmd, shell, 30)
	case "patch-check":
		plan, ok := patchPlanFor(s.store.osFor(agentID))
		if !ok {
			return false, "patching is not supported on this host"
		}
		return s.runReport(agentID, plan.status, plan.shell, 300)
	case "patch-install":
		plan, ok := patchPlanFor(s.store.osFor(agentID))
		if !ok {
			return false, "patching is not supported on this host"
		}
		return s.runReport(agentID, plan.install, plan.shell, plan.installTimeout)
	}
	return false, "unknown action " + action
}

// runReport runs a command on a host and reduces it to a verdict plus output.
//
// The output is trimmed to a tail: a fleet-wide patch run returns one of these
// per host, and an untrimmed apt transcript from twenty machines is a response
// nobody can read and a payload nobody wants.
func (s *Server) runReport(agentID, cmd, shell string, timeout int) (bool, string) {
	res, err := s.runOnAgent(agentID, cmd, shell, timeout)
	if err != nil {
		return false, err.Error()
	}
	out := tailLines(strings.TrimSpace(res.Stdout+"\n"+res.Stderr), 12)
	return res.ExitCode == 0 && res.Err == "", out
}
