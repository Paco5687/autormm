package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestHostPrefsPersist(t *testing.T) {
	dir := t.TempDir()
	p := newHostPrefs(dir)
	if err := p.set("nas", HostPref{Mem: 95, Note: "ZFS ARC eats RAM on purpose"}); err != nil {
		t.Fatal(err)
	}
	// A fresh store over the same directory must see it: the hub restarts.
	again := newHostPrefs(dir)
	if got := again.get("nas"); got.Mem != 95 || got.Note == "" {
		t.Errorf("prefs did not survive a restart: %+v", got)
	}
	if _, err := filepath.Abs(dir); err != nil {
		t.Fatal(err)
	}
	// Clearing back to defaults removes the entry rather than leaving clutter.
	_ = again.set("nas", HostPref{})
	if got := newHostPrefs(dir).get("nas"); !got.isZero() {
		t.Errorf("cleared prefs persisted: %+v", got)
	}
}

// The case this exists for: a host that legitimately runs hot must be able to
// stop paging without raising the threshold for the whole fleet.
func TestPerHostThresholdOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	prefs := newHostPrefs(dir)
	_ = prefs.set("nas", HostPref{Mem: 97})

	a := NewAlerter(AlertConfig{Mem: 90, For: time.Minute, OfflineAfter: time.Minute})
	a.prefs = prefs
	cur := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return cur }

	// Both hosts sit at 94%: over the global threshold, under the NAS override.
	views := []protocol.HostView{
		{AgentID: "nas", Hostname: "nas", Online: true, Metrics: &protocol.Metrics{MemPercent: 94}},
		{AgentID: "web", Hostname: "web", Online: true, Metrics: &protocol.Metrics{MemPercent: 94}},
	}
	a.evaluate(views) // starts the sustain clock
	cur = cur.Add(2 * time.Minute)
	tr := a.evaluate(views)
	if len(tr) != 1 {
		t.Fatalf("expected exactly one alert (web), got %d: %+v", len(tr), tr)
	}
	if tr[0].AgentID != "web" {
		t.Errorf("alerted on %q; the host with the override should have stayed quiet", tr[0].AgentID)
	}
}

// Muting must cover offline too — that is the alert people most want silenced
// while a machine is deliberately down.
func TestMutedHostRaisesNothingIncludingOffline(t *testing.T) {
	dir := t.TempDir()
	prefs := newHostPrefs(dir)
	cur := time.Unix(1_000_000, 0)
	_ = prefs.set("nas", HostPref{MuteUntil: cur.Add(2 * time.Hour)})

	a := NewAlerter(AlertConfig{CPU: 50, For: time.Minute, OfflineAfter: time.Minute})
	a.prefs = prefs
	a.now = func() time.Time { return cur }

	down := []protocol.HostView{{AgentID: "nas", Hostname: "nas", Online: false,
		Metrics: &protocol.Metrics{CPUPercent: 99}}}
	// Long enough that an unmuted host would certainly have fired.
	for i := 0; i < 5; i++ {
		if tr := a.evaluate(down); len(tr) != 0 {
			t.Fatalf("a muted host alerted: %+v", tr)
		}
		cur = cur.Add(time.Minute)
	}

	// The mute expires on its own — there is no cleanup step to forget.
	cur = cur.Add(3 * time.Hour)
	a.evaluate(down) // starts the sustain clock now that rules exist again
	cur = cur.Add(2 * time.Minute)
	if tr := a.evaluate(down); len(tr) == 0 {
		t.Error("alerts did not resume after the mute window expired")
	}
}
