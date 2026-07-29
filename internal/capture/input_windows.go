//go:build windows

package capture

import (
	"log"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// moveLogN counts how many pointer moves have been logged; the first few carry
// the virtual-screen metrics and computed absolute coords so a mis-mapped cursor
// (e.g. pinned to an edge) is diagnosable from the agent log. Guarded by the
// injector mutex that MouseMove already holds.
var moveLogN int

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procSendInput    = user32.NewProc("SendInput")
	procGetSystemMet = user32.NewProc("GetSystemMetrics")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	smCXScreen = 0
	smCYScreen = 1

	// Virtual-screen metrics (bounding box of all monitors).
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	mouseeventfMove        = 0x0001
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000

	keyeventfKeyUp   = 0x0002
	keyeventfUnicode = 0x0004

	wheelDelta = 120
)

// INPUT-sized structures (40 bytes each on amd64), padded so unsafe.Sizeof
// matches the Win32 INPUT union.
type mouseInputEvent struct {
	typ         uint32
	_pad0       uint32
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	_pad1       uint32
	dwExtraInfo uintptr
}

type keyInputEvent struct {
	typ         uint32
	_pad0       uint32
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	_pad1       uint32
	dwExtraInfo uintptr
	_tail       uint64
}

type winInjector struct {
	mu sync.Mutex
}

func newInjector() (Injector, error) { return &winInjector{}, nil }

func systemMetric(i int) int {
	r, _, _ := procGetSystemMet.Call(uintptr(i))
	// GetSystemMetrics returns a 32-bit signed int. Metrics like
	// SM_?VIRTUALSCREEN are negative when a monitor sits left of / above the
	// primary; int(r) zero-extends the DWORD and turns e.g. -2067 into
	// 4294965229, which wrecks the coordinate mapping. Sign-extend via int32.
	return int(int32(r))
}

// SendInput targets the calling thread's desktop, so in console-worker mode
// these must run on the thread following the input desktop — otherwise clicks
// and keystrokes vanish at the lock screen.
func sendMouse(e *mouseInputEvent) {
	e.typ = inputMouse
	onInputDesktop(func() {
		procSendInput.Call(1, uintptr(unsafe.Pointer(e)), unsafe.Sizeof(*e))
	})
}

func sendKey(e *keyInputEvent) {
	e.typ = inputKeyboard
	onInputDesktop(func() {
		procSendInput.Call(1, uintptr(unsafe.Pointer(e)), unsafe.Sizeof(*e))
	})
}

func (in *winInjector) MouseMove(x, y int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	// SendInput with MOUSEEVENTF_ABSOLUTE|VIRTUALDESK reaches the secure desktop
	// (SetCursorPos does not reliably), but needs the pixels normalised to
	// 0..65535 over the virtual screen. Run the whole thing on the
	// desktop-following thread so the metrics reflect the active desktop.
	onInputDesktop(func() {
		vx := systemMetric(smXVirtualScreen)
		vy := systemMetric(smYVirtualScreen)
		vw := systemMetric(smCXVirtualScreen)
		vh := systemMetric(smCYVirtualScreen)
		dw, dh := vw, vh
		if dw < 2 {
			dw = 2
		}
		if dh < 2 {
			dh = 2
		}
		ax := int32((x - vx) * 65535 / (dw - 1))
		ay := int32((y - vy) * 65535 / (dh - 1))
		e := &mouseInputEvent{dx: ax, dy: ay, dwFlags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk}
		e.typ = inputMouse
		n, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(e)), unsafe.Sizeof(*e))
		if moveLogN < 6 { // log the first few moves so a bad mapping is visible
			moveLogN++
			log.Printf("input move #%d: src=(%d,%d) vscreen=(x%d y%d w%d h%d) -> abs=(%d,%d) injected=%d following=%v",
				moveLogN, x, y, vx, vy, vw, vh, ax, ay, n, isFollowing())
		}
	})
	return nil
}

func (in *winInjector) MouseButton(button int, down bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	var flag uint32
	switch button {
	case 0:
		flag = mouseeventfLeftUp
		if down {
			flag = mouseeventfLeftDown
		}
	case 1:
		flag = mouseeventfMiddleUp
		if down {
			flag = mouseeventfMiddleDown
		}
	case 2:
		flag = mouseeventfRightUp
		if down {
			flag = mouseeventfRightDown
		}
	default:
		return nil
	}
	sendMouse(&mouseInputEvent{dwFlags: flag})
	return nil
}

func (in *winInjector) Scroll(dx, dy int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if dy != 0 {
		// Browser deltaY>0 means scrolling down; Windows wheel-down is negative.
		sendMouse(&mouseInputEvent{mouseData: uint32(int32(-dy * wheelDelta)), dwFlags: mouseeventfWheel})
	}
	if dx != 0 {
		sendMouse(&mouseInputEvent{mouseData: uint32(int32(dx * wheelDelta)), dwFlags: mouseeventfHWheel})
	}
	return nil
}

func (in *winInjector) Key(code string, down bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	vk, ok := codeToVK[code]
	if !ok {
		return nil
	}
	var flags uint32
	if !down {
		flags = keyeventfKeyUp
	}
	sendKey(&keyInputEvent{wVk: vk, dwFlags: flags})
	return nil
}

// TypeText injects Unicode text via KEYEVENTF_UNICODE (scan code = code unit),
// which types any character regardless of keyboard layout — used by the mobile
// on-screen keyboard.
func (in *winInjector) TypeText(text string) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, u16 := range utf16.Encode([]rune(text)) {
		sendKey(&keyInputEvent{wScan: u16, dwFlags: keyeventfUnicode})
		sendKey(&keyInputEvent{wScan: u16, dwFlags: keyeventfUnicode | keyeventfKeyUp})
	}
	return nil
}

func (in *winInjector) Close() error { return nil }
