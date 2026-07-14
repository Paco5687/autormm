//go:build windows

package capture

import (
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

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
	return int(r)
}

func sendMouse(e *mouseInputEvent) {
	e.typ = inputMouse
	procSendInput.Call(1, uintptr(unsafe.Pointer(e)), unsafe.Sizeof(*e))
}

func sendKey(e *keyInputEvent) {
	e.typ = inputKeyboard
	procSendInput.Call(1, uintptr(unsafe.Pointer(e)), unsafe.Sizeof(*e))
}

func (in *winInjector) MouseMove(x, y int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	// Map absolute virtual-desktop pixels to the 0..65535 range SendInput wants,
	// normalised against the whole virtual screen (all monitors) so any display,
	// at any offset, lands correctly. This fraction is DPI-independent.
	vx := systemMetric(smXVirtualScreen)
	vy := systemMetric(smYVirtualScreen)
	vw := systemMetric(smCXVirtualScreen)
	vh := systemMetric(smCYVirtualScreen)
	if vw < 2 {
		vw = 2
	}
	if vh < 2 {
		vh = 2
	}
	ax := int32((x - vx) * 65535 / (vw - 1))
	ay := int32((y - vy) * 65535 / (vh - 1))
	sendMouse(&mouseInputEvent{dx: ax, dy: ay, dwFlags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk})
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
