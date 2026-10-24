package capture

import "testing"

// Off Windows (and on Windows before FollowInputDesktop is called) work runs
// inline on the caller's goroutine — the ordinary agent path must be untouched.
//
// The reported completion matters as much as the run: a caller that reads a
// result out of the function must be able to tell a finished job from one that
// timed out and may still be writing.
func TestDesktopDispatchRunsInline(t *testing.T) {
	for name, dispatch := range map[string]func(func()) bool{
		"input":   onInputDesktop,
		"capture": onCaptureDesktop,
		"cursor":  onCursorDesktop,
	} {
		ran := false
		if ok := dispatch(func() { ran = true }); !ok {
			t.Errorf("%s dispatch reported an incomplete job when running inline", name)
		}
		if !ran {
			t.Errorf("%s dispatch did not run the function", name)
		}
	}
}
