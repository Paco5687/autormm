//go:build !windows

package capture

// screenLocked is Windows-specific: only there does the OS swap to a separate
// secure desktop that a user-session process cannot capture. On Linux a locker
// is an ordinary fullscreen window, which captures fine.
func screenLocked() bool { return false }
