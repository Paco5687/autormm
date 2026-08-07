package server

import (
	"testing"

	"github.com/Paco5687/autormm/internal/protocol"
)

func hv(id string, online bool, ips ...string) protocol.HostView {
	return protocol.HostView{AgentID: id, Online: online, Facts: protocol.HostFacts{IPs: ips}}
}

// The wake packet is a broadcast frame: only a peer on the target's own
// segment can deliver it. Everything about relay selection follows from that.
func TestWOLRelaySelection(t *testing.T) {
	target := hv("sleeper", false, "192.0.2.50")
	all := []protocol.HostView{
		target,
		hv("same-lan", true, "192.0.2.10"),     // shares the /24: relay
		hv("other-lan", true, "198.51.100.10"), // different segment: cannot help
		hv("multi-homed", true, "203.0.113.5", "192.0.2.99"), // one leg on the LAN: relay
		hv("same-but-off", false, "192.0.2.11"),              // offline peers cannot shout
		hv("no-facts", true),                                 // never reported an IP
	}
	got := wolRelays(target, all)
	want := map[string]bool{"same-lan": true, "multi-homed": true}
	if len(got) != len(want) {
		t.Fatalf("relays = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("chose %q, which cannot reach the target's segment", id)
		}
	}
}

// The target must never be asked to wake itself — it is offline, and if a stale
// connection lingers, sending it the request would be nonsense.
func TestWOLNeverRelaysThroughTheTarget(t *testing.T) {
	target := hv("sleeper", true, "192.0.2.50") // even if it looks online
	got := wolRelays(target, []protocol.HostView{target})
	if len(got) != 0 {
		t.Errorf("the target was chosen as its own relay: %v", got)
	}
}

// IPv6 and junk in facts must be skipped, not crash relay matching.
func TestWOLRelaysIgnoreNonIPv4(t *testing.T) {
	target := hv("sleeper", false, "fe80::1", "not-an-ip", "192.0.2.50")
	all := []protocol.HostView{target, hv("peer", true, "fe80::2", "192.0.2.9")}
	got := wolRelays(target, all)
	if len(got) != 1 || got[0] != "peer" {
		t.Errorf("relays = %v, want [peer]", got)
	}
}
