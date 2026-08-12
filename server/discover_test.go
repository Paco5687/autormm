package server

import (
	"net"
	"sort"
	"strconv"
	"testing"
)

// Addresses sort numerically. Lexical order puts .10 before .9, which reads as
// a shuffled list to anyone scanning for a device.
func TestDiscoverySortsAddressesNumerically(t *testing.T) {
	got := []string{"192.0.2.10", "192.0.2.9", "192.0.2.100", "192.0.2.1"}
	sort.Slice(got, func(i, j int) bool { return lessIP(got[i], got[j]) })
	want := []string{"192.0.2.1", "192.0.2.9", "192.0.2.10", "192.0.2.100"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Something unparseable must not panic or reorder unpredictably.
	if lessIP("not-an-ip", "also-not") == lessIP("also-not", "not-an-ip") {
		t.Error("garbage addresses compare inconsistently")
	}
}

// Devices already watched, or carrying an agent, are marked rather than offered
// again — otherwise every sweep invites you to add your whole fleet twice.
func TestKnownAddressesCoversChecksAndAgents(t *testing.T) {
	s := testServer()
	s.netChecks = &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	c1 := &NetCheck{ID: "n1", Name: "core switch", Address: "192.0.2.2"}
	c2 := &NetCheck{ID: "n2", Name: "lab printer", Address: "192.0.2.99", MAC: "aa:bb:cc:dd:ee:ff"}
	s.netChecks.checks["n1"], s.netChecks.checks["n2"] = c1, c2
	s.netChecks.state["n1"] = &NetStatus{NetCheck: *c1}
	// A MAC-tracked device is known by where it was found, not by what was typed.
	s.netChecks.state["n2"] = &NetStatus{NetCheck: *c2, IP: "192.0.2.57"}

	known, why := s.knownAddresses()
	if !known["192.0.2.2"] {
		t.Error("a monitored device was not marked")
	}
	if !known["192.0.2.57"] {
		t.Error("a MAC-tracked device was not marked at the address it was found at")
	}
	if known["192.0.2.99"] {
		t.Error("marked the stale recorded address instead of the current one")
	}
	if why["192.0.2.2"] == "" {
		t.Error("no explanation of why it is already known")
	}
}

// Unmonitored devices come first: they are the only ones there is anything to
// do about.
func TestDiscoveryListsUnmonitoredFirst(t *testing.T) {
	out := []Discovered{
		{IP: "192.0.2.2", Monitored: true},
		{IP: "192.0.2.9", Monitored: false},
		{IP: "192.0.2.3", Monitored: true},
		{IP: "192.0.2.8", Monitored: false},
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Monitored != out[j].Monitored {
			return !out[i].Monitored
		}
		return lessIP(out[i].IP, out[j].IP)
	})
	if out[0].Monitored || out[1].Monitored {
		t.Fatalf("monitored devices sorted to the front: %+v", out)
	}
	if out[0].IP != "192.0.2.8" {
		t.Errorf("unmonitored not in address order: %v", out[0].IP)
	}
}

// The port probe reports only what actually answered.
func TestOpenPortsReportsOnlyListeningPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	saved := discoverPorts
	discoverPorts = []int{port}
	defer func() { discoverPorts = saved }()

	got := openPorts(t.Context(), "127.0.0.1")
	if len(got) != 1 || got[0] != port {
		t.Errorf("openPorts = %v, want [%d]", got, port)
	}

	// A port nothing listens on is not reported.
	ln.Close()
	if got := openPorts(t.Context(), "127.0.0.1"); len(got) != 0 {
		t.Errorf("reported a closed port: %v", got)
	}
}
