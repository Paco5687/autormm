//go:build windows

package capture

import (
	"log"
	"runtime"
	"sync"
)

var (
	procSetThreadDesktop = user32.NewProc("SetThreadDesktop")

	followingMu sync.RWMutex
	following   bool

	// Capture and input each get their own desktop-following thread. They must
	// not share one: the capture side blocks inside AcquireNextFrame waiting for
	// the screen to change, and a keystroke queued behind that wait would be
	// delayed by the whole timeout — which showed up as laggy, dropped typing.
	captureThread = &desktopThread{name: "capture"}
	inputThread   = &desktopThread{name: "input"}
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
	name string
	once sync.Once
	jobs chan func()
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
func (t *desktopThread) do(fn func()) {
	if !isFollowing() || t.jobs == nil {
		fn()
		return
	}
	done := make(chan struct{})
	t.jobs <- func() {
		defer close(done)
		fn()
	}
	<-done
}

// onInputDesktop runs fn on the thread reserved for input injection, so a
// keystroke never waits behind a screen capture.
func onInputDesktop(fn func()) { inputThread.do(fn) }

// onCaptureDesktop runs fn on the thread reserved for screen capture.
func onCaptureDesktop(fn func()) { captureThread.do(fn) }
