//go:build windows

package capture

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	procOpenInputDesktop  = user32.NewProc("OpenInputDesktop")
	procCloseDesktop      = user32.NewProc("CloseDesktop")
	procGetUserObjectInfo = user32.NewProc("GetUserObjectInformationW")
)

const (
	uoiName            = 2
	desktopReadObjects = 0x0001
	// Capturing and injecting on a desktop needs far more than read access.
	desktopGenericAll = 0x10000000
)

// inputDesktopName returns the name of the desktop currently receiving input.
// "Default" is the ordinary user desktop. "Winlogon" is the secure desktop
// Windows switches to when the workstation is locked, at the sign-in screen,
// or while a UAC prompt is up.
func inputDesktopName() (string, bool) {
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if h == 0 {
		return "", false
	}
	defer procCloseDesktop.Call(h)

	var buf [256]uint16
	var needed uint32
	r, _, _ := procGetUserObjectInfo.Call(h, uoiName,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf[:]), true
}

// screenLocked reports whether the host is showing a desktop this process
// cannot capture. A user-session agent is bound to the "Default" desktop, so
// once input moves to "Winlogon" the frames it grabs are blank.
//
// OpenInputDesktop failing is itself the answer: a process on the Default
// desktop is denied a handle to the secure desktop, so treat that as locked.
//
// The console worker follows the input desktop, so it can capture the secure
// desktop too — for it, nothing is ever "locked out", and reporting false keeps
// the viewer from showing a lock-screen notice over a perfectly good picture.
func screenLocked() bool {
	if isFollowing() {
		return false
	}
	name, ok := inputDesktopName()
	if !ok {
		return true
	}
	return !strings.EqualFold(name, "Default")
}
