//go:build linux || windows

package capture

import (
	"fmt"
	"image"

	"github.com/kbinani/screenshot"
)

func captureAvailable() bool { return screenshot.NumActiveDisplays() > 0 }

type screenCapturer struct{ display int }

func newScreenCapturer() (Capturer, error) {
	if screenshot.NumActiveDisplays() == 0 {
		return nil, fmt.Errorf("capture: no active display (is a graphical session running / DISPLAY set?)")
	}
	return &screenCapturer{display: 0}, nil
}

func (c *screenCapturer) Bounds() image.Rectangle {
	return screenshot.GetDisplayBounds(c.display)
}

func (c *screenCapturer) Capture() (*image.RGBA, error) {
	return screenshot.CaptureRect(c.Bounds())
}

func (c *screenCapturer) Close() error { return nil }
