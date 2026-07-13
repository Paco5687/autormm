// Package capture provides cross-platform screen capture and input injection,
// plus a tiling delta encoder that turns captured frames into the autormm
// binary screen-frame format.
//
// Capture + input are supported on Linux (X11/XTEST) and Windows (GDI/SendInput).
// On other platforms the constructors return an error but the package still
// compiles so the agent can run in metrics-only mode.
package capture

import (
	"image"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Capturer grabs a selectable region of the desktop (all displays, or one).
type Capturer interface {
	Bounds() image.Rectangle
	Capture() (*image.RGBA, error)
	Displays() []protocol.Display
	Select(index int) error // -1 = all displays (virtual desktop), 0..N-1 = one
	Close() error
}

// Injector synthesises input on the host. Coordinates are absolute screen pixels.
type Injector interface {
	MouseMove(x, y int) error
	MouseButton(button int, down bool) error // 0=left 1=middle 2=right
	Scroll(dx, dy int) error
	Key(code string, down bool) error // code is a JS KeyboardEvent.code
	Close() error
}

// Available reports whether screen capture is supported on this OS.
func Available() bool { return captureAvailable() }

// NewCapturer constructs a screen capturer for this platform.
func NewCapturer() (Capturer, error) { return newScreenCapturer() }

// NewInjector constructs an input injector for this platform.
func NewInjector() (Injector, error) { return newInjector() }
