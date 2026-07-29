//go:build !windows

package capture

// FollowInputDesktop is a Windows-only concept: elsewhere there is no separate
// secure desktop to follow, so capture and input always target the session the
// agent already runs in.
func FollowInputDesktop() {}

func onInputDesktop(fn func()) { fn() }
