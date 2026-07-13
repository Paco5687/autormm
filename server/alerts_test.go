package server

import (
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestAlerterHysteresis(t *testing.T) {
	a := NewAlerter(AlertConfig{CPU: 90, For: time.Minute, OfflineAfter: 30 * time.Second})
	cur := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return cur }

	view := func(cpu float64, online bool) []protocol.HostView {
		return []protocol.HostView{{
			AgentID: "h1", Hostname: "h1", Online: online,
			Metrics: &protocol.Metrics{CPUPercent: cpu, MemPercent: 10},
		}}
	}

	// High CPU, but not yet sustained for `For` — no alert.
	if tr := a.evaluate(view(95, true)); len(tr) != 0 {
		t.Fatalf("should not fire immediately: %+v", tr)
	}
	// After the sustain window, it fires.
	cur = cur.Add(61 * time.Second)
	tr := a.evaluate(view(95, true))
	if len(tr) != 1 || !tr[0].Firing || tr[0].Rule != "cpu" {
		t.Fatalf("expected cpu fire, got %+v", tr)
	}
	// Still elevated but within the clear margin — stays firing silently.
	cur = cur.Add(20 * time.Second)
	if tr := a.evaluate(view(93, true)); len(tr) != 0 {
		t.Fatalf("should stay firing without a new transition: %+v", tr)
	}
	if len(a.Active()) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(a.Active()))
	}
	// Drops below threshold-margin — resolves.
	tr = a.evaluate(view(80, true))
	if len(tr) != 1 || tr[0].Firing {
		t.Fatalf("expected cpu resolve, got %+v", tr)
	}
	if len(a.Active()) != 0 {
		t.Fatalf("expected no active alerts after resolve, got %d", len(a.Active()))
	}

	// Offline fires only after the offline window.
	if tr := a.evaluate(view(0, false)); len(tr) != 0 {
		t.Fatalf("offline should not fire immediately: %+v", tr)
	}
	cur = cur.Add(31 * time.Second)
	tr = a.evaluate(view(0, false))
	if len(tr) != 1 || !tr[0].Firing || tr[0].Rule != "offline" {
		t.Fatalf("expected offline fire, got %+v", tr)
	}
}
