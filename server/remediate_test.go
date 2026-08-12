package server

import (
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// A service that crashes in a loop must not have its restart script run every
// ten seconds forever.
func TestRemediationAttemptsAreBounded(t *testing.T) {
	m := newRemediator()
	now := time.Now()
	for i := 0; i < maxRemediationAttempts; i++ {
		if !m.allow("h1|service:docker", now) {
			t.Fatalf("attempt %d refused while still in budget", i+1)
		}
	}
	if m.allow("h1|service:docker", now) {
		t.Error("allowed an attempt beyond the budget")
	}
	// A different condition on the same host has its own budget.
	if !m.allow("h1|disk", now) {
		t.Error("one condition's budget consumed another's")
	}
}

// The budget is a rate, not a lifetime cap: a service that misbehaves once a
// week should be fixed every week.
func TestRemediationBudgetRecoversWithTime(t *testing.T) {
	m := newRemediator()
	now := time.Now()
	for i := 0; i < maxRemediationAttempts; i++ {
		m.allow("h1|service:docker", now)
	}
	if m.allow("h1|service:docker", now) {
		t.Fatal("budget not spent")
	}
	if !m.allow("h1|service:docker", now.Add(remediationWindow+time.Minute)) {
		t.Error("budget never recovers")
	}
}

func TestRemediationBudgetResetsOnRecovery(t *testing.T) {
	m := newRemediator()
	now := time.Now()
	for i := 0; i < maxRemediationAttempts; i++ {
		m.allow("h1|service:docker", now)
	}
	m.clear("h1|service:docker")
	if !m.allow("h1|service:docker", now) {
		t.Error("a recovered condition did not get its budget back")
	}
}

// The most specific configured fix wins, matching how thresholds resolve.
func TestRemediationResolvesHostOverPolicy(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"all":        {Remediate: map[string]string{"service:docker": "broad"}},
		"tag:server": {Remediate: map[string]string{"service:docker": "narrow"}},
		"web1":       {Remediate: map[string]string{"service:docker": "host"}},
	})
	if got := p.remediationFor(web1, "service:docker"); got != "host" {
		t.Errorf("got %q, want the host's own script", got)
	}

	p2 := prefsWith(map[string]HostPref{
		"all":        {Remediate: map[string]string{"service:docker": "broad"}},
		"tag:server": {Remediate: map[string]string{"service:docker": "narrow"}},
	})
	if got := p2.remediationFor(web1, "service:docker"); got != "narrow" {
		t.Errorf("got %q, want the tag policy to beat the all policy", got)
	}
	// A rule with nothing configured remediates nothing.
	if got := p2.remediationFor(web1, "cpu"); got != "" {
		t.Errorf("got %q for an unconfigured rule", got)
	}
}

// Holding an alert must not leave a resolution to be announced for something
// that was never sent, and the rule must be able to fire again if the fix did
// not work.
func TestHoldRewindsTheCondition(t *testing.T) {
	a := NewAlerter(AlertConfig{})
	a.svcStates = func(string) map[string]bool { return map[string]bool{"docker": false} }
	a.watched = func(protocol.HostView) []string { return []string{"docker"} }
	v := []protocol.HostView{{AgentID: "h1", Hostname: "web1", Online: true, OS: "linux"}}

	tr := a.evaluate(v)
	if len(tr) != 1 || !tr[0].Firing {
		t.Fatalf("expected one firing alert, got %+v", tr)
	}
	a.hold("h1", "service:docker")

	// Still stopped: it fires again rather than staying silent forever.
	if tr := a.evaluate(v); len(tr) != 1 || !tr[0].Firing {
		t.Errorf("a held alert did not fire again: %+v", tr)
	}

	// And when the fix works, no resolution is announced for an alert nobody
	// ever received.
	a2 := NewAlerter(AlertConfig{})
	states := map[string]bool{"docker": false}
	a2.svcStates = func(string) map[string]bool { return states }
	a2.watched = func(protocol.HostView) []string { return []string{"docker"} }
	a2.evaluate(v)
	a2.hold("h1", "service:docker")
	states["docker"] = true
	if tr := a2.evaluate(v); len(tr) != 0 {
		t.Errorf("announced something after a successful fix: %+v", tr)
	}
}
