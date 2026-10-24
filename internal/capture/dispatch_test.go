package capture

import (
	"sync"
	"testing"
	"time"
)

// worker runs a dispatcher's jobs until stopped, optionally stalling forever on
// the first one — which is what a Windows syscall that never returns does to
// the real thing.
func worker(d *dispatcher, wedgeOn int) (stop func()) {
	done := make(chan struct{})
	go func() {
		n := 0
		for job := range d.jobs {
			n++
			if n == wedgeOn {
				<-done // never comes back, exactly like the fault this guards
				return
			}
			job()
		}
	}()
	return func() { close(done) }
}

// A thread with no deadline waits as long as the work takes. Capture depends on
// this: blocking is how it waits for the screen to change, and a still screen
// blocks for the whole DXGI timeout on every frame.
func TestNoDeadlineWaitsForSlowWork(t *testing.T) {
	d := &dispatcher{name: "capture", jobs: make(chan func())}
	defer worker(d, 0)()

	var got string
	start := time.Now()
	ok := d.send(func() {
		time.Sleep(120 * time.Millisecond)
		got = "finished"
	})
	if !ok {
		t.Fatal("a job that finished was reported as incomplete")
	}
	if got != "finished" {
		t.Errorf("the caller was released before the job wrote its result: %q", got)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Error("did not actually wait")
	}
}

// A wedged worker must cost one event, not every event after it. This is the
// fault that took a session's input away entirely while video kept running,
// because capture has a thread of its own and never noticed.
func TestAWedgedWorkerDoesNotBlockLaterCallers(t *testing.T) {
	d := &dispatcher{name: "input", jobs: make(chan func()), timeout: 40 * time.Millisecond}
	defer worker(d, 1)() // the very first job never returns

	if ok := d.send(func() {}); ok {
		t.Error("the wedged job was reported as completed")
	}
	// The point: everything after it still returns rather than queueing behind
	// a worker that is never coming back.
	for i := 0; i < 3; i++ {
		start := time.Now()
		if ok := d.send(func() {}); ok {
			t.Errorf("call %d claimed to complete against a wedged worker", i)
		}
		if waited := time.Since(start); waited > 300*time.Millisecond {
			t.Errorf("call %d waited %v — it is queueing behind the wedge", i, waited)
		}
	}
}

// The completion flag is the contract that makes a timeout safe: a caller that
// reads a result must be able to tell a finished job from one still writing.
func TestIncompleteJobsAreReportedSoResultsAreNotRead(t *testing.T) {
	d := &dispatcher{name: "cursor", jobs: make(chan func()), timeout: 30 * time.Millisecond}
	release := make(chan struct{})
	stop := worker(d, 0)
	defer stop()

	var mu sync.Mutex
	var result string
	ok := d.send(func() {
		<-release
		mu.Lock()
		result = "late"
		mu.Unlock()
	})
	if ok {
		t.Fatal("a job still running was reported as complete")
	}
	// A caller honouring the flag never reads result, which is the whole point:
	// the job is still going to write it.
	close(release)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if result != "late" {
		t.Errorf("the abandoned job did not finish on its own: %q", result)
	}
}

// Stalls must be visible without drowning the log they would be diagnosed from.
func TestStallsAreLoggedAtMostOncePerSecond(t *testing.T) {
	d := &dispatcher{name: "input"}
	first := d.lastWarn
	d.report("busy")
	if d.lastWarn.Equal(first) {
		t.Fatal("the first stall was not reported")
	}
	at := d.lastWarn
	for i := 0; i < 50; i++ {
		d.report("busy")
	}
	if !d.lastWarn.Equal(at) {
		t.Error("repeated stalls logged more than once a second")
	}
}
