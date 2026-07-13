//go:build windows

package capture

import (
	"errors"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")

	procOpenClipboard     = user32.NewProc("OpenClipboard")
	procCloseClipboard    = user32.NewProc("CloseClipboard")
	procEmptyClipboard    = user32.NewProc("EmptyClipboard")
	procGetClipboardData  = user32.NewProc("GetClipboardData")
	procSetClipboardData  = user32.NewProc("SetClipboardData")
	procIsClipFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var errClipboard = errors.New("clipboard unavailable")

func getClipboard() (string, bool) {
	if r, _, _ := procIsClipFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", false
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return "", false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	return utf16PtrToString(p), true
}

func setClipboard(text string) error {
	u16, err := syscall.UTF16FromString(sanitizeNUL(text))
	if err != nil {
		return err
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return errClipboard
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(u16)*2))
	if h == 0 {
		return errClipboard
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return errClipboard
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u16))
	copy(dst, u16)
	procGlobalUnlock.Call(h)

	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return errClipboard // system did not take ownership
	}
	return nil // on success the system owns the memory
}

func utf16PtrToString(p uintptr) string {
	var u16 []uint16
	for i := uintptr(0); ; i += 2 {
		c := *(*uint16)(unsafe.Pointer(p + i))
		if c == 0 {
			break
		}
		u16 = append(u16, c)
	}
	return string(utf16.Decode(u16))
}

func sanitizeNUL(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			b := []byte(s)
			for j := range b {
				if b[j] == 0 {
					b[j] = ' '
				}
			}
			return string(b)
		}
	}
	return s
}
