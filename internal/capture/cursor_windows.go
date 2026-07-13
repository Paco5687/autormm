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

func (winCursor) Pos() (int, int, bool, bool) {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if r == 0 {
		return 0, 0, false, false
	}
	return int(ci.ptScreenPos.X), int(ci.ptScreenPos.Y), ci.flags&cursorShowing != 0, true
}

func (winCursor) Close() error { return nil }
