package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// An agent that starts before its graphical session registers can_stream=false,
// and that answer only travels at registration — so the connection itself has
// to end for the hub to learn better. Without this the host sits with Remote
// greyed out until something unrelated drops the socket, which on a desktop
// that logs itself in at boot is the normal way round.
func TestWaitForDisplayEndsTheConnectionOnceAScreenAppears(t *testing.T) {
	defer restoreDisplayVars(displayPoll, captureAvailable)
	displayPoll = time.Millisecond

	var polls int64
	captureAvailable = func() bool { return atomic.AddInt64(&polls, 1) >= 3 }

	ended := make(chan struct{})
	a := &Agent{}
	go a.waitForDisplay(context.Background(), func() { close(ended) })

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the connection was never ended after a display appeared")
	}
	if n := atomic.LoadInt64(&polls); n < 3 {
		t.Errorf("gave up after %d polls; it should keep looking until there is a display", n)
	}
}

// And it belongs to the connection that started it: a reconnect loop that left
// one of these behind per attempt would poll for the life of the process.
func TestWaitForDisplayStopsWithTheConnection(t *testing.T) {
	defer restoreDisplayVars(displayPoll, captureAvailable)
	displayPoll = time.Millisecond
	captureAvailable = func() bool { return false }

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	a := &Agent{}
	go func() {
		a.waitForDisplay(ctx, func() { t.Error("reconnected although no display appeared") })
		close(stopped)
	}()

	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForDisplay outlived the connection it was watching for")
	}
}

func restoreDisplayVars(poll time.Duration, available func() bool) {
	displayPoll, captureAvailable = poll, available
}
