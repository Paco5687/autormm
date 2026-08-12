package server

import (
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Auto-remediation: run a script when a condition fires, and only alert if it
// did not help.
//
// The pattern every mature RMM settles on. A monitor that can only say "docker
// is down" makes a person get up and restart it; a monitor that restarts it
// first and escalates only on failure is the difference between a tool that
// reports problems and one that fixes them.
//
// The sequencing matters more than the mechanism. When a rule fires and a
// remediation is configured, the alert is HELD rather than sent: the script
// runs, the condition is re-checked, and the alert is only dispatched on the
// next evaluation if it is still true. A fix that works produces no alert at
// all rather than an alert immediately followed by a resolution, which is the
// noise this is meant to remove.

const (
	// Attempts are bounded per rule so a service that crashes in a loop cannot
	// have its restart script run every ten seconds forever.
	maxRemediationAttempts = 3
	remediationWindow      = time.Hour
)

// remediator tracks how often each condition has been auto-fixed lately.
type remediator struct {
	mu       sync.Mutex
	attempts map[string][]time.Time // "agent|rule" -> attempt times
}

func newRemediator() *remediator {
	return &remediator{attempts: map[string][]time.Time{}}
}

// allow reports whether another attempt is within budget, recording it if so.
func (m *remediator) allow(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.attempts[key][:0]
	for _, t := range m.attempts[key] {
		if now.Sub(t) < remediationWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxRemediationAttempts {
		m.attempts[key] = kept
		return false
	}
	m.attempts[key] = append(kept, now)
	return true
}

// clear forgets a condition's history, so a host that has been healthy for a
// while starts again with a full budget.
func (m *remediator) clear(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, key)
}

// remediationFor returns the script configured to fix a rule on this host, from
// the host's own settings or a policy covering it.
func (h *hostPrefs) remediationFor(v protocol.HostView, ruleName string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Host first, then narrower policies, then broader: the most specific
	// configured fix wins, matching how thresholds resolve.
	if s, ok := h.m[v.AgentID].Remediate[ruleName]; ok && s != "" {
		return s
	}
	var keys []string
	for key := range h.m {
		if IsSelector(key) && matchesTarget(key, v) {
			keys = append(keys, key)
		}
	}
	best, bestRank := "", -1
	for _, k := range keys {
		if s, ok := h.m[k].Remediate[ruleName]; ok && s != "" {
			if r := policyRank(k); r > bestRank {
				best, bestRank = s, r
			}
		}
	}
	return best
}

// tryRemediate runs the configured fix for a firing alert.
//
// Returns true when the alert should be held back — a script ran and the next
// evaluation will decide whether it worked. False means dispatch now: nothing
// is configured, the budget is spent, or the script could not run at all.
func (s *Server) tryRemediate(a Alert) bool {
	key := a.AgentID + "|" + a.Rule
	if !a.Firing {
		// Recovered: give this condition its full budget back, so a service
		// that misbehaves once a week is fixed every week rather than three
		// times ever.
		s.remediator.clear(key)
		return false
	}
	if s.prefs == nil || s.scripts == nil {
		return false
	}
	var v protocol.HostView
	for _, hv := range s.store.views() {
		if hv.AgentID == a.AgentID {
			v = hv
			break
		}
	}
	if v.AgentID == "" || !v.Online {
		return false // nothing can be run on a host that is not there
	}
	scriptID := s.prefs.remediationFor(v, a.Rule)
	if scriptID == "" {
		return false
	}
	if !s.remediator.allow(key, time.Now()) {
		// Budget spent: the fix is not working, so stop trying and let the
		// alert through. Saying so in the trail matters — silently giving up
		// looks identical to never having tried.
		s.auditLog.record(AuditEvent{
			TS: time.Now().Unix(), Actor: "auto-remediation", Action: "remediate",
			Target: a.AgentID, Detail: a.Rule + ": giving up after repeated attempts",
			Outcome: "failed",
		})
		return false
	}
	sc, err := s.scripts.GetScript(scriptID)
	if err != nil {
		return false
	}

	run := s.runScript(sc, a.AgentID, "remediation")
	outcome := "ok"
	if run.ExitCode != 0 || run.Error != "" {
		outcome = "failed"
	}
	s.auditLog.record(AuditEvent{
		TS: time.Now().Unix(), Actor: "auto-remediation", Action: "remediate",
		Target: a.AgentID, Detail: a.Rule + " → " + sc.Name, Outcome: outcome,
	})

	// A watched service is polled once a minute, so without re-reading it here
	// the next evaluation would still see the stale "stopped" and alert anyway.
	s.repollAfterFix(v)
	return true
}

// repollAfterFix refreshes whatever the next evaluation will read, so a fix
// that worked is visible immediately rather than a poll interval later.
func (s *Server) repollAfterFix(v protocol.HostView) {
	names := s.watchedServicesFor(v)
	if len(names) == 0 {
		return
	}
	shell, cmd, ok := serviceStatusCommand(v.OS, names)
	if !ok {
		return
	}
	res, err := s.runOnAgent(v.AgentID, cmd, shell, 30)
	if err != nil {
		return
	}
	s.svc.set(v.AgentID, parseServiceStates(res.Stdout))
}
