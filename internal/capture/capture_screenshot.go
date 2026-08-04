//go:build linux || windows

package capture

import (
	"fmt"
	"image"
	"log"
	"sync"

	"github.com/kbinani/screenshot"

	"github.com/Paco5687/autormm/internal/protocol"
)

func captureAvailable() bool { return screenshot.NumActiveDisplays() > 0 }

// frameSource is an optional accelerated capture backend. On Windows this is
// DXGI Desktop Duplication; elsewhere there is none and the GDI path is used
// directly. grab returns ErrNoChange when the desktop has not been repainted.
type frameSource interface {
	grab(region image.Rectangle) (*image.RGBA, error)
	// dirty reports what changed in the last successful grab, in image
	// coordinates; nil means "assume everything".
	dirty() []image.Rectangle
	close()
}

type screenCapturer struct {
	mu     sync.Mutex
	region image.Rectangle
	sel    int // -1 = all displays, else display index

	// fast is the accelerated backend, dropped permanently if it ever fails so a
	// host with a quirky driver degrades to the GDI path instead of breaking.
	fast frameSource
	// dirty is what the last Capture reported as changed; nil when the backend
	// cannot say (the GDI/X11 path never can).
	dirty []image.Rectangle
}

// Dirty reports the regions changed by the most recent Capture.
func (c *screenCapturer) Dirty() []image.Rectangle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty
}

func newScreenCapturer() (Capturer, error) {
	if screenshot.NumActiveDisplays() == 0 {
		return nil, fmt.Errorf("capture: no active display (is a graphical session running / DISPLAY set?)")
	}
	c := &screenCapturer{fast: newFastSource()}
	// Default to one display rather than the union of all of them: streaming a
	// multi-monitor desktop as a single frame is enormous (two 4K panels is ~21
	// megapixels) and too slow to be usable. The viewer switches between them.
	c.Select(primaryDisplay())
	return c, nil
}

// primaryDisplay returns the index of the display at the virtual-desktop origin,
// falling back to the first one.
func primaryDisplay() int {
	for i, n := 0, screenshot.NumActiveDisplays(); i < n; i++ {
		if b := screenshot.GetDisplayBounds(i); b.Min.X == 0 && b.Min.Y == 0 {
			return i
		}
	}
	return 0
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
			Modes:   displayModes(i),
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

// Selected returns the display index currently being captured (-1 if the whole
// desktop, which the viewer no longer offers but the protocol still allows).
func (c *screenCapturer) Selected() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sel
}

func (c *screenCapturer) Bounds() image.Rectangle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.region
}

func (c *screenCapturer) Capture() (*image.RGBA, error) {
	// In console-worker mode this runs on the capture thread, which follows the
	// active input desktop — so the lock / sign-in screen captures like any
	// other, and a blocking wait for the next frame never delays input, which
	// has its own thread. The accelerated backend is pinned here because its
	// device and duplication belong to the desktop they were created on.
	var img *image.RGBA
	var err error
	region := c.Bounds()
	onCaptureDesktop(func() {
		if c.fast != nil {
			img, err = c.fast.grab(region)
			if err == nil || err == ErrNoChange {
				c.setDirty(c.fast.dirty())
				return
			}
			log.Printf("capture: accelerated backend failed (%v) -- using GDI from here on", err)
			c.fast.close()
			c.fast = nil
		}
		// The screenshot path cannot report damage, so the encoder must assume
		// the whole frame changed.
		c.setDirty(nil)
		img, err = screenshot.CaptureRect(region)
	})
	return img, err
}

func (c *screenCapturer) setDirty(d []image.Rectangle) {
	c.mu.Lock()
	c.dirty = d
	c.mu.Unlock()
}

func (c *screenCapturer) Close() error {
	if c.fast != nil {
		c.fast.close()
		c.fast = nil
	}
	return nil
}
