//go:build linux

package capture

import "image"

// newFastSource reports no accelerated backend on Linux: X11 has no equivalent
// of Desktop Duplication here, so the screenshot path is used directly and the
// encoder is told nothing about damage. Tagged linux (not !windows) because
// frameSource only exists where screenCapturer does — macOS builds neither.
func newFastSource() frameSource { return nil }

// Kept so the interface stays satisfied if a Linux backend is added later.
var _ = func() []image.Rectangle { return nil }
