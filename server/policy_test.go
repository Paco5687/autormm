package server

import (
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

func prefsWith(m map[string]HostPref) *hostPrefs { return &hostPrefs{m: m} }

var web1 = protocol.HostView{AgentID: "web1", OS: "linux", Tags: "prod, server", Online: true}

// The point of a policy: set a threshold once for a group instead of walking
// the fleet setting it machine by machine.
func TestPolicyAppliesToEveryMatchingHost(t *testing.T) {
	p := prefsWith(map[string]HostPref{"tag:server": {Disk: 95}})
	if got := p.resolve(web1).Disk; got != 95 {
		t.Errorf("disk = %v, want the tag policy's 95", got)
	}
	other := protocol.HostView{AgentID: "pc1", OS: "windows", Tags: "office"}
	if got := p.resolve(other).Disk; got != 0 {
		t.Errorf("policy leaked to a host outside the tag: %v", got)
	}
}

// A machine's own setting is the last word. Otherwise the one box you tuned
// silently reverts the moment somebody writes a policy.
func TestHostOverrideBeatsPolicy(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"all":        {CPU: 80, Disk: 80},
		"tag:server": {Disk: 95},
		"web1":       {Disk: 99},
	})
	got := p.resolve(web1)
	if got.Disk != 99 {
		t.Errorf("disk = %v, want the host's own 99", got.Disk)
	}
	// Fields the host did not set still come from the policies below it.
	if got.CPU != 80 {
		t.Errorf("cpu = %v, want 80 inherited from the all policy", got.CPU)
	}
}

// Narrower beats broader, so "every host" can set a floor that specific groups
// raise.
func TestNarrowerPolicyWins(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"all":        {Disk: 70},
		"os:linux":   {Disk: 85},
		"tag:server": {Disk: 95},
	})
	if got := p.resolve(web1).Disk; got != 95 {
		t.Errorf("disk = %v, want the tag policy to win", got)
	}
}

// Two tag policies can match one host. Whichever wins, it must be the same one
// on every evaluation — map order would otherwise flap the threshold and make
// an alert fire and resolve at random.
func TestOverlappingPoliciesAreDeterministic(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"tag:prod":   {CPU: 90},
		"tag:server": {CPU: 60},
	})
	first := p.resolve(web1).CPU
	for i := 0; i < 200; i++ {
		if got := p.resolve(web1).CPU; got != first {
			t.Fatalf("resolution flapped: %v then %v", first, got)
		}
	}
}

// Silence is an act on one machine. A policy must not be able to mute a whole
// tag, because nothing could then un-mute a single host inside it.
func TestMuteIsNotInherited(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"tag:server": {Mute: true},
		"web1":       {CPU: 50},
	})
	if p.resolve(web1).Silenced(time.Now()) {
		t.Error("a policy silenced a host")
	}
	p2 := prefsWith(map[string]HostPref{"web1": {Mute: true}})
	if !p2.resolve(web1).Silenced(time.Now()) {
		t.Error("the host's own mute was dropped")
	}
}

// A fleet with no policies at all behaves exactly as before.
func TestNoPoliciesMeansNoChange(t *testing.T) {
	p := prefsWith(map[string]HostPref{"web1": {CPU: 55}})
	got := p.resolve(web1)
	if got.CPU != 55 || got.Mem != 0 || got.Disk != 0 {
		t.Errorf("unexpected resolution: %+v", got)
	}
}
