package server

import (
	"testing"

	"github.com/Paco5687/autormm/internal/protocol"
)

func fleet() []protocol.HostView {
	return []protocol.HostView{
		{AgentID: "web1", OS: "linux", Tags: "prod, linux", Online: true},
		{AgentID: "web2", OS: "linux", Tags: "Prod Linux", Online: true},      // different typing
		{AgentID: "pc1", OS: "windows", Tags: "office;windows", Online: true}, // semicolons
		{AgentID: "old", OS: "linux", Tags: "prod", Online: false},            // offline
		{AgentID: "bare", OS: "darwin", Online: true},                         // no tags at all
	}
}

func TestResolvePlainAgentIDPassesThrough(t *testing.T) {
	got := resolveTarget("web1", fleet())
	if len(got) != 1 || got[0] != "web1" {
		t.Errorf("resolveTarget(agent id) = %v", got)
	}
	// Even for a host that is offline: naming a host explicitly is a deliberate
	// act, and the run should report its own failure rather than vanish.
	if got := resolveTarget("old", fleet()); len(got) != 1 {
		t.Errorf("an explicitly named offline host was dropped: %v", got)
	}
}

// Tags are free text typed by a person, so the selector must survive the ways
// people actually type them.
func TestTagSelectorToleratesRealTagStrings(t *testing.T) {
	got := resolveTarget("tag:linux", fleet())
	want := map[string]bool{"web1": true, "web2": true}
	if len(got) != len(want) {
		t.Fatalf("tag:linux matched %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("tag:linux wrongly matched %q", id)
		}
	}
	if got := resolveTarget("tag:PROD", fleet()); len(got) != 2 {
		t.Errorf("tag matching is case-sensitive: %v", got)
	}
	if got := resolveTarget("tag:windows", fleet()); len(got) != 1 || got[0] != "pc1" {
		t.Errorf("semicolon-separated tags not matched: %v", got)
	}
}

// A tag must match whole, or "tag:lin" would silently sweep in every Linux box.
func TestTagSelectorDoesNotMatchPartialTags(t *testing.T) {
	if got := resolveTarget("tag:lin", fleet()); len(got) != 0 {
		t.Errorf("a partial tag matched hosts: %v", got)
	}
	if got := resolveTarget("tag:", fleet()); len(got) != 0 {
		t.Errorf("an empty tag matched hosts: %v", got)
	}
}

// Offline hosts are excluded from selectors: a script cannot run on a machine
// that is not connected, and counting it as targeted reports a success that
// never happened.
func TestSelectorsSkipOfflineHosts(t *testing.T) {
	for _, sel := range []string{"all", "tag:prod", "os:linux"} {
		for _, id := range resolveTarget(sel, fleet()) {
			if id == "old" {
				t.Errorf("%s included the offline host", sel)
			}
		}
	}
}

func TestOSSelector(t *testing.T) {
	got := resolveTarget("os:windows", fleet())
	if len(got) != 1 || got[0] != "pc1" {
		t.Errorf("os:windows = %v", got)
	}
	if got := resolveTarget("os:LINUX", fleet()); len(got) != 2 {
		t.Errorf("os matching is case-sensitive: %v", got)
	}
}

func TestKnownTagsDeduplicatesAcrossTyping(t *testing.T) {
	tags := knownTags(fleet())
	seen := map[string]int{}
	for _, tg := range tags {
		seen[lower(tg)]++
	}
	for tg, n := range seen {
		if n > 1 {
			t.Errorf("tag %q listed %d times", tg, n)
		}
	}
	if len(seen) != 4 { // prod, linux, office, windows
		t.Errorf("knownTags = %v, want 4 distinct", tags)
	}
}

func lower(s string) string {
	b := []rune(s)
	for i, r := range b {
		if r >= 'A' && r <= 'Z' {
			b[i] = r + 32
		}
	}
	return string(b)
}
