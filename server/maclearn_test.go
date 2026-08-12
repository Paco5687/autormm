package server

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// A device watched by address alone gets its MAC filled in, so it keeps working
// when DHCP moves it.
func TestMACIsLearnedForAReachableDevice(t *testing.T) {
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
	port, _ := strconv.Atoi(portStr)

	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	n.macs.byMAC = map[string]string{"aa:bb:cc:dd:ee:ff": "127.0.0.1"}
	n.macs.updated, n.macs.swept = time.Now(), time.Now()
	n.checks["d"] = &NetCheck{ID: "d", Name: "switch", Address: "127.0.0.1", Port: port}

	n.runChecks(context.Background(), time.Now())

	got := n.checks["d"]
	if got.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want it learned", got.MAC)
	}
	if !got.MACLearned {
		t.Error("a learned MAC was not marked as learned")
	}
}

// Not for a device that did not answer: the ARP entry for that address may
// belong to whoever holds it now, and binding a check to the wrong machine
// permanently is worse than not binding it at all.
func TestMACIsNotLearnedFromAnUnreachableDevice(t *testing.T) {
	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	n.macs.byMAC = map[string]string{"aa:bb:cc:dd:ee:ff": "192.0.2.77"}
	n.macs.updated, n.macs.swept = time.Now(), time.Now()
	// Nothing is listening on this documentation address.
	n.checks["d"] = &NetCheck{ID: "d", Name: "ghost", Address: "192.0.2.77", Port: 9}

	n.runChecks(context.Background(), time.Now())

	if got := n.checks["d"]; got.MAC != "" {
		t.Errorf("learned %q from a device that never answered", got.MAC)
	}
}

// A MAC that was typed in is authoritative: not finding it means the device is
// not there. A learned one only means ARP forgot, and the address still stands.
func TestLearnedMACDoesNotForceADeviceDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
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
	port, _ := strconv.Atoi(portStr)

	// Empty index: neither MAC can be resolved.
	mk := func(learned bool) []NetStatus {
		n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
		n.macs.updated, n.macs.swept = time.Now(), time.Now()
		n.checks["d"] = &NetCheck{
			ID: "d", Name: "dev", Address: "127.0.0.1", Port: port,
			MAC: "aa:bb:cc:dd:ee:ff", MACLearned: learned,
		}
		return n.runChecks(context.Background(), time.Now())
	}

	if got := mk(true); len(got) != 1 || !got[0].Up {
		t.Error("a learned MAC that could not be resolved marked a reachable device down")
	}
	if got := mk(false); len(got) != 1 || got[0].Up {
		t.Error("a typed-in MAC that could not be resolved was ignored")
	}
}

// Apps are URLs, not machines on a LAN; there is no MAC to learn.
func TestAppsDoNotLearnMACs(t *testing.T) {
	if canLearnMAC([]NetCheck{{Kind: "app", Address: "example.com"}}) {
		t.Error("an app was considered for MAC learning")
	}
	if !canLearnMAC([]NetCheck{{Address: "192.0.2.2"}}) {
		t.Error("a plain device was not considered")
	}
	if canLearnMAC([]NetCheck{{Address: "192.0.2.2", MAC: "aa:bb:cc:dd:ee:ff"}}) {
		t.Error("a device that already has a MAC was reconsidered")
	}
}
