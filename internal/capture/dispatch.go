package capture

import (
	"log"
	"sync"
	"time"
)

// Handing work to a thread that must not move.
//
// Some Windows state is per-thread — which desktop a thread can see, and
// therefore what it can capture, inject into, or read the cursor from — so that
// work has to run on one pinned OS thread rather than wherever the scheduler
// puts a goroutine. This is the half of that arrangement with no Windows in it:
// the handing over, the waiting, and the deciding when to stop waiting. It
// lives here so it can be tested on any machine, because the semantics below
// are easy to get wrong and were, twice.

// dispatcher sends work to a worker goroutine and waits for it.
type dispatcher struct {
	name string
	jobs chan func()
	// timeout bounds how long a caller waits. Zero waits indefinitely, which is
	// right for work whose whole job is to block until something happens.
	timeout time.Duration

	warnMu   sync.Mutex
	lastWarn time.Time
}

// send hands fn to the worker and reports whether it ran to completion.
//
// The return value is not decoration. On a timeout the job may still be
// running and still writing whatever it was going to write, so a caller that
// reads a result out of fn must check this first — otherwise it reads a value
// while another thread is writing it. Callers that only fire and forget can
// ignore it.
func (d *dispatcher) send(fn func()) bool {
	done := make(chan struct{})
	job := func() {
		defer close(done)
		fn()
	}
	if d.timeout == 0 {
		d.jobs <- job
		<-done
		return true
	}
	select {
	case d.jobs <- job:
	case <-time.After(d.timeout):
		// The worker is inside something that has not come back. Do not join
		// the queue behind it: an unbounded wait here is how one stalled
		// syscall becomes a session that never accepts input again.
		d.report("busy: gave up handing off a job")
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(d.timeout):
		// Accepted but still running. The caller is released; the job will
		// finish in its own time, into memory the caller must not read.
		d.report("slow: a job has not returned")
		return false
	}
}

// report logs a stall at most once a second, so a wedged thread leaves a trail
// without burying the log it would be diagnosed from.
func (d *dispatcher) report(what string) {
	d.warnMu.Lock()
	defer d.warnMu.Unlock()
	if time.Since(d.lastWarn) < time.Second {
		return
	}
	d.lastWarn = time.Now()
	log.Printf("desktop-follow(%s): %s", d.name, what)
}
