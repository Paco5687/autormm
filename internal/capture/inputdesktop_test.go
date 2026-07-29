package capture

import "testing"

// Off Windows (and on Windows before FollowInputDesktop is called) work runs
// inline on the caller's goroutine — the ordinary agent path must be untouched.
func TestOnInputDesktopRunsInline(t *testing.T) {
	ran := false
	onInputDesktop(func() { ran = true })
	if !ran {
		t.Fatal("onInputDesktop did not run the function")
	}
}
