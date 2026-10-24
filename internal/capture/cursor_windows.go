//go:build windows

package capture

import "unsafe"

// user32 is declared in input_windows.go (same package).
var procGetCursorInfo = user32.NewProc("GetCursorInfo")

const cursorShowing = 0x00000001

type winPoint struct{ X, Y int32 }

type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     uintptr
	ptScreenPos winPoint
}

type winCursor struct{}

func newCursor() (Cursor, error) { return &winCursor{}, nil }

// Pos reads the pointer on the thread that can see it.
//
// GetCursorInfo is scoped to the calling thread's desktop, so a reader left on
// whichever desktop the process started on reports a desktop with no cursor:
// hidden, and with a position that never changes. Capture and injection have
// always followed the input desktop; this did not, which is the whole of that
// bug.
func (winCursor) Pos() (int, int, bool, bool) {
	var got struct {
		x, y    int
		visible bool
		ok      bool
	}
	// The result is only read when the job finished; a timed-out job may still
	// be writing into it.
	if !onCursorDesktop(func() {
		var ci cursorInfo
		ci.cbSize = uint32(unsafe.Sizeof(ci))
		r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
		if r == 0 {
			return
		}
		got.x, got.y = int(ci.ptScreenPos.X), int(ci.ptScreenPos.Y)
		got.visible, got.ok = ci.flags&cursorShowing != 0, true
	}) {
		return 0, 0, false, false
	}
	return got.x, got.y, got.visible, got.ok
}

func (winCursor) Close() error { return nil }
