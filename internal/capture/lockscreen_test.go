package capture

import "testing"

// On non-Windows platforms a screen locker is an ordinary window, so capture
// keeps working and no notice should ever be raised.
func TestScreenLockedNonWindows(t *testing.T) {
	if ScreenLocked() {
		t.Error("ScreenLocked() = true on a platform without a secure desktop")
	}
}
