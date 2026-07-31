package capture

import "testing"

// Off Windows (and on Windows before FollowInputDesktop is called) work runs
// inline on the caller's goroutine — the ordinary agent path must be untouched.
func TestDesktopDispatchRunsInline(t *testing.T) {
	for name, dispatch := range map[string]func(func()){
		"input":   onInputDesktop,
		"capture": onCaptureDesktop,
	} {
		ran := false
		dispatch(func() { ran = true })
		if !ran {
			t.Errorf("%s dispatch did not run the function", name)
		}
	}
}
