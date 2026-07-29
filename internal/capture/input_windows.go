//go:build windows

package capture

import (
	"log"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// firstMoveLog logs the virtual-screen metrics the first time a pointer move is
// injected, so a mis-mapped cursor (e.g. pinned to an edge) is diagnosable from
// the agent log without guesswork.
var firstMoveLog sync.Once

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procSendInput    = user32.NewProc("SendInput")
	procGetSystemMet = user32.NewProc("GetSystemMetrics")
	procSetCursorPos = user32.NewProc("SetCursorPos")
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
	// SetCursorPos takes exact virtual-desktop pixels (x,y already include the
	// captured region's origin), so there is no 0..65535 normalisation to get
	// wrong — the earlier SendInput/VIRTUALDESK mapping collapsed one axis when
	// the virtual-screen metrics came back off. It must still run on the
	// desktop-following thread (a no-op for the ordinary agent) or it targets the
	// wrong desktop. Negative coordinates (a monitor left of/above the primary)
	// pass through correctly as int32 in the low word.
	onInputDesktop(func() {
		r, _, _ := procSetCursorPos.Call(uintptr(uint32(int32(x))), uintptr(uint32(int32(y))))
		firstMoveLog.Do(func() {
			log.Printf("input: first mouse move -> SetCursorPos(%d,%d) ok=%v following=%v", x, y, r != 0, isFollowing())
		})
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
