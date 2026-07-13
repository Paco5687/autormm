//go:build linux || windows

package capture

import (
	"fmt"
	"image"
	"sync"

	"github.com/kbinani/screenshot"

	"github.com/Paco5687/autormm/internal/protocol"
)

func captureAvailable() bool { return screenshot.NumActiveDisplays() > 0 }

type screenCapturer struct {
	mu     sync.Mutex
	region image.Rectangle
	sel    int // -1 = all displays, else display index
}

func newScreenCapturer() (Capturer, error) {
	if screenshot.NumActiveDisplays() == 0 {
		return nil, fmt.Errorf("capture: no active display (is a graphical session running / DISPLAY set?)")
	}
	c := &screenCapturer{}
	c.Select(-1) // default: the whole desktop (all displays)
	return c, nil
}

// Displays enumerates the active monitors.
func (c *screenCapturer) Displays() []protocol.Display {
	n := screenshot.NumActiveDisplays()
	out := make([]protocol.Display, 0, n)
	for i := 0; i < n; i++ {
		b := screenshot.GetDisplayBounds(i)
		out = append(out, protocol.Display{
			Index: i, X: b.Min.X, Y: b.Min.Y, W: b.Dx(), H: b.Dy(),
			Primary: b.Min.X == 0 && b.Min.Y == 0, // the (0,0) display is the primary
		})
	}
	return out
}

// Select points the capturer at all displays (-1) or a single display.
func (c *screenCapturer) Select(index int) error {
	n := screenshot.NumActiveDisplays()
	var region image.Rectangle
	if index < 0 {
		for i := 0; i < n; i++ {
			region = region.Union(screenshot.GetDisplayBounds(i))
		}
	} else if index < n {
		region = screenshot.GetDisplayBounds(index)
	} else {
		return fmt.Errorf("capture: no display %d (have %d)", index, n)
	}
	c.mu.Lock()
	c.region, c.sel = region, index
	c.mu.Unlock()
	return nil
}

func (c *screenCapturer) Bounds() image.Rectangle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.region
}

func (c *screenCapturer) Capture() (*image.RGBA, error) {
	return screenshot.CaptureRect(c.Bounds())
}

func (c *screenCapturer) Close() error { return nil }
