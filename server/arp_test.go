package server

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// macOS renders leading zeros away in `arp -a`, so its table is full of short
// octets. ParseMAC rejects those, which would mean silently reading no table at
// all on a macOS hub.
func TestNormalizeMACAcceptsShortOctets(t *testing.T) {
	if got := normalizeMAC("0:11:22:aa:bb:cc"); got != "00:11:22:aa:bb:cc" {
		t.Errorf("macOS-style MAC = %q, want 00:11:22:aa:bb:cc", got)
	}
	if got := normalizeMAC("0:0:0:0:0:1"); got != "00:00:00:00:00:01" {
		t.Errorf("all-short MAC = %q", got)
	}
}

func TestNormalizeMACAcceptsWhatPeopleType(t *testing.T) {
	want := "aa:bb:cc:dd:ee:ff"
	for _, in := range []string{
		"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF",
		"aa-bb-cc-dd-ee-ff", "AA-BB-CC-DD-EE-FF",
		"  aa:bb:cc:dd:ee:ff  ",
		"aa:bb:cc:dd:ee:ff",
	} {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "not-a-mac", "aa:bb:cc:dd:ee", "hello world"} {
		if got := normalizeMAC(bad); got != "" {
			t.Errorf("normalizeMAC(%q) = %q, want empty", bad, got)
		}
	}
}

// The kernel lists addresses it tried and failed to resolve with an all-zero
// MAC and no complete flag. Counting those would invent devices that are not
// there, and worse, map several of them onto one bogus MAC.
func TestARPLineMatchesRealOutput(t *testing.T) {
	// A line from `arp -a` on Linux, macOS and Windows respectively.
	for _, line := range []string{
		"gateway (192.0.2.1) at 64:62:66:23:c2:28 [ether] on eth0",
		"? (192.168.1.1) at 0:11:22:aa:bb:cc on en0 ifscope [ethernet]",
		"  192.168.1.1           00-11-22-aa-bb-cc     dynamic",
	} {
		m := arpLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("no match in %q", line)
			continue
		}
		if net.ParseIP(m[1]) == nil {
			t.Errorf("%q: bad IP %q", line, m[1])
		}
		if normalizeMAC(m[2]) == "" {
			t.Errorf("%q: bad MAC %q", line, m[2])
		}
	}
}

// Sweeping must never walk a network too large to finish — a /16 is 65,000
// addresses, and the container bridges that usually carry one hold nothing
// worth finding.
func TestSweepSkipsHugeNetworks(t *testing.T) {
	for _, n := range sweepableNets() {
		ones, bits := n.Mask.Size()
		if bits != 32 || ones < 22 {
			t.Errorf("would sweep %v (/%d) — too large", n, ones)
		}
	}
}

func TestNextIPWalksAndCarries(t *testing.T) {
	got := nextIP(net.ParseIP("10.0.0.1").To4())
	if got.String() != "10.0.0.2" {
		t.Errorf("nextIP(10.0.0.1) = %v", got)
	}
	if got := nextIP(net.ParseIP("10.0.0.255").To4()); got.String() != "10.0.1.0" {
		t.Errorf("carry failed: %v", got)
	}
	// Must not scribble on the caller's address, which would corrupt the loop
	// bounds it is being walked against.
	orig := net.ParseIP("10.0.0.1").To4()
	_ = nextIP(orig)
	if orig.String() != "10.0.0.1" {
		t.Errorf("nextIP mutated its argument: %v", orig)
	}
}

// The real table on this machine, if any, must parse into plausible pairs.
func TestReadARPProducesUsablePairs(t *testing.T) {
	for mac, ip := range readARP() {
		if normalizeMAC(mac) != mac {
			t.Errorf("key %q is not normalised", mac)
		}
		if net.ParseIP(ip) == nil {
			t.Errorf("mac %s maps to %q, which is not an IP", mac, ip)
		}
		if mac == "00:00:00:00:00:00" {
			t.Error("an unresolved entry was kept")
		}
	}
}

// A device that is switched off never resolves, so the "sweep when something
// is missing" rule would otherwise mean sweeping on every single check cycle
// for as long as it stays off.
func TestSweepIsRateLimited(t *testing.T) {
	m := newMACIndex()
	m.swept = time.Now()
	before := m.swept
	m.refresh(context.Background(), true)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.swept.Equal(before) {
		t.Error("swept again inside the minimum interval")
	}
}

// End-to-end: a device recorded by MAC is checked at whatever address it holds
// right now, and the card reports that address.
func TestMACTrackedDeviceIsCheckedAtItsCurrentAddress(t *testing.T) {
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
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)

	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	n.macs.byMAC = map[string]string{"aa:bb:cc:dd:ee:ff": "127.0.0.1"}
	n.macs.updated = time.Now() // seeded table stands in for a real read
	n.macs.swept = time.Now()   // the address is already known; do not sweep
	n.checks["d"] = &NetCheck{ID: "d", Name: "printer", MAC: "aa:bb:cc:dd:ee:ff", Port: p}

	got := n.runChecks(context.Background(), time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if !got[0].Up {
		t.Errorf("device down: %v", got[0].Error)
	}
	if got[0].IP != "127.0.0.1" {
		t.Errorf("IP = %q, want the address it was found at", got[0].IP)
	}
}

// The failure this guards: falling back to a recorded address when the MAC is
// nowhere to be found means checking whoever holds that address now, and
// reporting a different machine's health under this device's name.
func TestMACTrackedDeviceWithNoMatchIsNotReportedUp(t *testing.T) {
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
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)

	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	n.macs.updated = time.Now()
	n.macs.swept = time.Now()
	// Address of the *old* lease, now held by something else that answers.
	n.checks["d"] = &NetCheck{ID: "d", Name: "printer", MAC: "aa:bb:cc:dd:ee:ff", Address: "127.0.0.1", Port: p}

	got := n.runChecks(context.Background(), time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Up {
		t.Error("reported up from a stale address with the MAC missing")
	}
	if got[0].Error == "" {
		t.Error("no explanation of why it could not be checked")
	}
}
