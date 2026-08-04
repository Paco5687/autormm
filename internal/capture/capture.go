// Package capture provides cross-platform screen capture and input injection,
// plus a tiling delta encoder that turns captured frames into the autormm
// binary screen-frame format.
//
// Capture + input are supported on Linux (X11/XTEST) and Windows (GDI/SendInput).
// On other platforms the constructors return an error but the package still
// compiles so the agent can run in metrics-only mode.
package capture

import (
	"errors"
	"image"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Capturer grabs a selectable region of the desktop (all displays, or one).
type Capturer interface {
	Bounds() image.Rectangle
	Capture() (*image.RGBA, error)
	Displays() []protocol.Display
	Select(index int) error // -1 = all displays (virtual desktop), 0..N-1 = one
	Selected() int          // the display currently captured
	// Dirty reports the regions that changed in the most recent Capture, in
	// image coordinates. nil means the backend cannot say, so callers must
	// assume the whole frame changed.
	Dirty() []image.Rectangle
	// EventDriven reports whether Capture blocks until the screen actually
	// changes. When it does, the caller must not also sleep between frames: the
	// wait is already happening in the right place, and sleeping afterwards just
	// delays noticing the next change.
	EventDriven() bool
	Close() error
}

// Injector synthesises input on the host. MouseMove coordinates are absolute
// pixels in the virtual desktop (i.e. the captured region's origin already
// added), so callers must offset region-relative coordinates before calling.
type Injector interface {
	MouseMove(x, y int) error
	MouseButton(button int, down bool) error // 0=left 1=middle 2=right
	Scroll(dx, dy int) error
	Key(code string, down bool) error // code is a JS KeyboardEvent.code
	TypeText(text string) error       // type Unicode text (e.g. from a soft keyboard)
	Close() error
}

// Available reports whether screen capture is supported on this OS.
func Available() bool { return captureAvailable() }

// ScreenLocked reports whether the host is currently showing a desktop this
// process cannot capture (the Windows lock/sign-in screen). Callers use it to
// explain a blank stream instead of sending black frames.
func ScreenLocked() bool { return screenLocked() }

// NewCapturer constructs a screen capturer for this platform.
func NewCapturer() (Capturer, error) { return newScreenCapturer() }

// NewInjector constructs an input injector for this platform.
func NewInjector() (Injector, error) { return newInjector() }

// ErrNoChange is returned by a Capturer when the desktop has not been repainted
// since the previous frame. Callers should skip encoding rather than re-sending
// an identical picture; it is not a failure.
var ErrNoChange = errors.New("capture: no change")
