//go:build !linux && !windows

package capture

// keyTable is empty on platforms with no input injection; the tests that use it
// skip rather than fail.
func keyTable() map[string]bool { return nil }
