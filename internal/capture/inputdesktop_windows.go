//go:build windows

package capture

import (
	"log"
	"runtime"
	"sync"
)

var (
	procSetThreadDesktop = user32.NewProc("SetThreadDesktop")

	desktopOnce sync.Once
	desktopJobs chan func()
	followingMu sync.RWMutex
	following   bool
)

// FollowInputDesktop switches this process into console-worker mode: screen
// capture and input injection are dispatched onto a dedicated OS thread that
// re-attaches itself to whichever desktop currently has input.
//
// Windows scopes both GDI capture and SendInput to the calling thread's
// desktop, and it swaps the whole desktop (to "Winlogon") when the machine
// locks, shows the sign-in screen, or raises a UAC prompt. A thread left on
// "Default" quietly captures blank frames and injects into nothing. Only a
// process running as SYSTEM inside the console session may attach to the secure
// desktop, so this is a no-op unless the console worker enabled it.
func FollowInputDesktop() {
	desktopOnce.Do(func() {
		desktopJobs = make(chan func())
		ready := make(chan struct{})
		go desktopThread(ready)
		<-ready
	})
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
func desktopThread(ready chan<- struct{}) {
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
			log.Printf("desktop-follow: OpenInputDesktop(%q) failed", name)
			return
		}
		// SetThreadDesktop fails (ERROR_BUSY) if the thread still holds GDI/DC
		// state on its current desktop, which is exactly the case after a capture
		// — the symptom is a thread stuck on Winlogon after unlock, so the screen
		// and pointer freeze. Log it so that failure is visible.
		if r, _, err := procSetThreadDesktop.Call(h); r == 0 {
			log.Printf("desktop-follow: SetThreadDesktop(%q) failed: %v (staying on %q)", name, err, currentName)
			procCloseDesktop.Call(h)
			return
		}
		if current != 0 {
			procCloseDesktop.Call(current)
		}
		current, currentName = h, name
		log.Printf("desktop-follow: now on %q", name)
	}

	attach()
	close(ready)
	for job := range desktopJobs {
		attach() // the desktop can swap between any two frames
		job()
	}
}

// onInputDesktop runs fn on the desktop-following thread and waits for it. When
// the follower is not enabled it runs fn inline, so the ordinary user-session
// agent keeps its existing behaviour.
func onInputDesktop(fn func()) {
	if !isFollowing() {
		fn()
		return
	}
	done := make(chan struct{})
	desktopJobs <- func() {
		defer close(done)
		fn()
	}
	<-done
}
