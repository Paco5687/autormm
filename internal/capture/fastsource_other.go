//go:build !windows

package capture

// newFastSource reports no accelerated backend outside Windows: X11 capture has
// no equivalent of Desktop Duplication here, so the GDI/X11 path is used.
func newFastSource() frameSource { return nil }
