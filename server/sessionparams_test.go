package server

import (
	"strings"
	"testing"
)

// An out-of-range framerate must be clamped to the nearest allowed value, never
// collapsed to something far below the default. See sessionFPS for what that
// cost when it was not true.
func TestSessionFPSIsClampedNotCollapsed(t *testing.T) {
	for _, tc := range []struct {
		asked, want int
		why         string
	}{
		{asked: 60, want: maxSessionFPS, why: "above the ceiling: clamp down to it"},
		{asked: 5000, want: maxSessionFPS, why: "absurd: still just the ceiling"},
		{asked: 30, want: 30, why: "in range: honoured exactly"},
		{asked: 5, want: 5, why: "a deliberately low request is honoured"},
		{asked: 0, want: defaultFPS, why: "unset: the default"},
		{asked: -1, want: defaultFPS, why: "nonsense: the default"},
	} {
		if got := sessionFPS(tc.asked); got != tc.want {
			t.Errorf("sessionFPS(%d) = %d, want %d (%s)", tc.asked, got, tc.want, tc.why)
		}
	}
	// The specific regression: a request above the ceiling must never come back
	// lower than the default.
	if got := sessionFPS(60); got < defaultFPS {
		t.Errorf("asking for more than the ceiling returned %d, below the default %d", got, defaultFPS)
	}
}

// The dashboard and the viewer create sessions from separate call sites and
// drifted apart once already: the viewer was moved to 30 fps while the
// dashboard — which opens every session — was left asking for 60, and the hub
// then quietly turned that into 10.
func TestTheWebClientsAgreeOnFramerate(t *testing.T) {
	for _, asset := range []string{"web/app.js", "web/viewer.js"} {
		b, err := webFS.ReadFile(asset)
		if err != nil {
			t.Fatalf("read %s: %v", asset, err)
		}
		if !strings.Contains(string(b), "fps: 30") {
			t.Errorf("%s does not request 30 fps; both clients must agree and stay inside the hub's range", asset)
		}
	}
	if got := sessionFPS(30); got != 30 {
		t.Errorf("the framerate both web clients ask for is not honoured: got %d", got)
	}
}
