//go:build !linux && !windows

package capture

import "fmt"

func captureAvailable() bool { return false }

func newScreenCapturer() (Capturer, error) {
	return nil, fmt.Errorf("capture: screen capture not supported on this platform")
}

func newInjector() (Injector, error) {
	return nil, fmt.Errorf("capture: input injection not supported on this platform")
}

func newCursor() (Cursor, error) {
	return nil, fmt.Errorf("capture: cursor reading not supported on this platform")
}
