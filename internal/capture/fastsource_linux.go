//go:build linux

package capture

// newFastSource reports no accelerated backend on Linux: X11 has no equivalent
// of Desktop Duplication here, so the screenshot path is used directly. Tagged
// linux (not !windows) because frameSource only exists where screenCapturer
// does — macOS builds neither.
func newFastSource() frameSource { return nil }
