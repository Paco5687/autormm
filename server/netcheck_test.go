package server

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// A device that answers is up, with a latency figure.
func TestProbeReachableDevice(t *testing.T) {
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
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p := atoiTest(t, port)

	up, ms, _, err := probe(context.Background(), host, p)
	if !up || err != nil {
		t.Fatalf("a listening device reported down: up=%v err=%v", up, err)
	}
	if ms < 0 {
		t.Errorf("negative latency: %v", ms)
	}
}

// The case that separates a useful check from a noisy one: a closed port on a
// live host means the host is *up*. Reporting it down would page the operator
// every time a port guess was wrong.
func TestProbeTreatsRefusedAsUp(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // nothing is listening now, but the host answers

	p := atoiTest(t, port)
	up, _, _, _ := probe(context.Background(), "127.0.0.1", p)
	if !up {
		t.Error("a refused connection was reported as an unreachable device")
	}
}

// Silence is the only thing that means gone.
func TestProbeUnreachableAddressIsDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	// RFC 5737 documentation range: guaranteed not to be routable anywhere.
	up, _, _, err := probe(ctx, "192.0.2.1", 9)
	if up {
		t.Error("an unroutable address reported as up")
	}
	if err == nil {
		t.Error("no error recorded for an unreachable device")
	}
}

// State transitions drive notifications, so only real changes may be reported.
func TestRunChecksReportsOnlyTransitions(t *testing.T) {
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
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p := atoiTest(t, port)

	n := newNetChecks("")
	n.checks["x"] = &NetCheck{ID: "x", Name: "switch", Address: host, Port: p, Interval: 1}

	now := time.Now()
	if ch := n.runChecks(context.Background(), now); len(ch) != 1 {
		t.Fatalf("first probe should report a result, got %d", len(ch))
	}
	// Same state, interval elapsed: nothing to say.
	now = now.Add(2 * time.Second)
	if ch := n.runChecks(context.Background(), now); len(ch) != 0 {
		t.Errorf("an unchanged device reported a transition: %+v", ch)
	}
	// It goes away: that is a transition.
	//
	// Closing the listener would not do it — that yields a refused connection,
	// which this deliberately treats as up (the host answered; the port is
	// merely shut). Vanishing means silence, so point the check at an
	// unroutable address instead.
	n.mu.Lock()
	n.checks["x"].Address = "192.0.2.1"
	n.mu.Unlock()
	now = now.Add(2 * time.Second)
	ch := n.runChecks(context.Background(), now)
	if len(ch) != 1 || ch[0].Up {
		t.Errorf("a device going silent did not report down: %+v", ch)
	}
}

// Checks must survive a hub restart.
func TestNetChecksPersist(t *testing.T) {
	dir := t.TempDir()
	n := newNetChecks(dir)
	n.checks["a"] = &NetCheck{ID: "a", Name: "printer", Address: "192.0.2.9", Port: 9100}
	if err := n.save(); err != nil {
		t.Fatal(err)
	}
	again := newNetChecks(dir)
	list := again.list()
	if len(list) != 1 || list[0].Name != "printer" || list[0].Port != 9100 {
		t.Errorf("checks did not survive a restart: %+v", list)
	}
	// Never probed yet: reported, but not claimed to be up.
	if list[0].Up {
		t.Error("an unprobed check claimed to be up")
	}
}

func atoiTest(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
