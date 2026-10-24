//go:build windows

package capture

import (
	"log"
	"runtime"
	"sync"
	"time"
)

var (
	procSetThreadDesktop = user32.NewProc("SetThreadDesktop")

	followingMu sync.RWMutex
	following   bool

	// Capture and input each get their own desktop-following thread. They must
	// not share one: the capture side blocks inside AcquireNextFrame waiting for
	// the screen to change, and a keystroke queued behind that wait would be
	// delayed by the whole timeout — which showed up as laggy, dropped typing.
	// Capture has no deadline on purpose: blocking is how it waits for the
	// screen to change, and a still screen blocks for the full DXGI timeout
	// every frame. Nothing queues behind it — that is what its own thread is
	// for — so waiting costs nothing but the frame it is already waiting on.
	captureThread = &desktopThread{dispatcher: dispatcher{name: "capture"}}
	inputThread   = &desktopThread{dispatcher: dispatcher{name: "input", timeout: jobTimeout}}
	// Reading the pointer is desktop-scoped too, and for the same reason: the
	// cursor a thread can see belongs to its own desktop. Without this the
	// reader sits on whatever desktop the process started on and reports a
	// desktop with no cursor at all — hidden, position frozen, handle frozen —
	// which the viewer then faithfully renders as a pointer that vanishes,
	// flickers and wears the wrong shape.
	//
	// Its own thread rather than sharing: it polls at 30Hz, and putting that
	// in front of injection would make every keystroke queue behind a cursor
	// read.
	cursorThread = &desktopThread{dispatcher: dispatcher{name: "cursor", timeout: jobTimeout}}
)

// FollowInputDesktop switches this process into console-worker mode: screen
// capture and input injection are dispatched onto dedicated OS threads that
// re-attach themselves to whichever desktop currently has input.
//
// Windows scopes both GDI/DXGI capture and SendInput to the calling thread's
// desktop, and it swaps the whole desktop (to "Winlogon") when the machine
// locks, shows the sign-in screen, or raises a UAC prompt. A thread left on
// "Default" quietly captures blank frames and injects into nothing. Only a
// process running as SYSTEM inside the console session may attach to the secure
// desktop, so this is a no-op unless the console worker enabled it.
func FollowInputDesktop() {
	captureThread.start()
	inputThread.start()
	cursorThread.start()
	followingMu.Lock()
	following = true
	followingMu.Unlock()
}

func isFollowing() bool {
	followingMu.RLock()
	defer followingMu.RUnlock()
	return following
}

// desktopThread owns a single OS thread for the lifetime of the process. The
// thread's desktop assignment is per-thread state, which is exactly why it must
// never be released back to the Go scheduler.
type desktopThread struct {
	dispatcher
	once sync.Once
}

func (t *desktopThread) start() {
	t.once.Do(func() {
		t.jobs = make(chan func())
		ready := make(chan struct{})
		go t.run(ready)
		<-ready
	})
}

func (t *desktopThread) run(ready chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var current uintptr // desktop handle this thread is attached to
	currentName := ""
	attach := func() {
		name, ok := inputDesktopName()
		if !ok || name == currentName {
			return // cannot see the input desktop, or already on it
		}
		h, _, _ := procOpenInputDesktop.Call(0, 0, desktopGenericAll)
		if h == 0 {
			log.Printf("desktop-follow(%s): OpenInputDesktop(%q) failed", t.name, name)
			return
		}
		// SetThreadDesktop fails (ERROR_BUSY) if the thread still holds GDI/DC
		// state on its current desktop, which is exactly the case after a capture
		// — the symptom is a thread stuck on Winlogon after unlock, so the screen
		// and pointer freeze. Log it so that failure is visible.
		if r, _, err := procSetThreadDesktop.Call(h); r == 0 {
			log.Printf("desktop-follow(%s): SetThreadDesktop(%q) failed: %v (staying on %q)", t.name, name, err, currentName)
			procCloseDesktop.Call(h)
			return
		}
		if current != 0 {
			procCloseDesktop.Call(current)
		}
		current, currentName = h, name
		log.Printf("desktop-follow(%s): now on %q", t.name, name)
	}

	attach()
	close(ready)
	for job := range t.jobs {
		attach() // the desktop can swap between any two jobs
		job()
	}
}

// do runs fn on this thread and waits for it. When following is not enabled it
// runs inline, so the ordinary user-session agent is unaffected.
// jobTimeout is how long callers of the input and cursor threads will wait.
//
// Those dispatch Windows syscalls documented to return promptly that
// occasionally do not: SendInput blocks on a full input queue, and SetCursorPos
// and the desktop switch can block on a busy desktop. Waiting forever on an
// unbuffered channel turns one such stall into a session with no input at all —
// permanently, since the worker never returns to its loop and every later
// caller queues behind it — while capture carries on from its own thread and
// the picture looks perfectly healthy. A dropped event is recoverable; a dead
// input path is not.
//
// Generous by the standards of the work: injection is sub-millisecond and
// reading the pointer barely more, so a quarter second means something is
// wrong rather than something is busy.
const jobTimeout = 250 * time.Millisecond

// do runs fn on this thread and reports whether it completed. When following is
// not enabled it runs inline on the caller's goroutine, so the ordinary
// user-session agent is untouched by any of this.
func (t *desktopThread) do(fn func()) bool {
	if !isFollowing() || t.jobs == nil {
		fn()
		return true
	}
	return t.send(fn)
}

// onInputDesktop runs fn on the thread reserved for input injection, so a
// keystroke never waits behind a screen capture.
func onInputDesktop(fn func()) bool { return inputThread.do(fn) }

// onCursorDesktop runs fn on the thread reserved for reading the pointer.
func onCursorDesktop(fn func()) bool { return cursorThread.do(fn) }

// onCaptureDesktop runs fn on the thread reserved for screen capture.
func onCaptureDesktop(fn func()) bool { return captureThread.do(fn) }
