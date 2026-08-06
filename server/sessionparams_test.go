package server

import (
	"strconv"
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
// then quietly turned that into 10. Nothing about that was visible; it just
// looked like a slow host.
//
// So pin every session parameter across all three places it is written: both
// web clients and the hub's own default.
func TestTheWebClientsAgreeWithTheHubDefaults(t *testing.T) {
	for _, asset := range []string{"web/app.js", "web/viewer.js"} {
		b, err := webFS.ReadFile(asset)
		if err != nil {
			t.Fatalf("read %s: %v", asset, err)
		}
		src := string(b)
		if !strings.Contains(src, "fps: "+strconv.Itoa(defaultFPS)) {
			t.Errorf("%s does not request %d fps; both clients and the hub default must agree", asset, defaultFPS)
		}
		if !strings.Contains(src, "quality: "+strconv.Itoa(defaultQuality)) {
			t.Errorf("%s does not request quality %d; both clients and the hub default must agree", asset, defaultQuality)
		}
	}
	if got := sessionFPS(defaultFPS); got != defaultFPS {
		t.Errorf("the framerate both web clients ask for is not honoured: got %d", got)
	}
	if got := sessionQuality(defaultQuality); got != defaultQuality {
		t.Errorf("the quality both web clients ask for is not honoured: got %d", got)
	}
}

// The slider must start at the value a session actually opens with, or the
// control lies about the state of the stream from the first frame.
func TestTheQualitySliderStartsAtTheDefault(t *testing.T) {
	b, err := webFS.ReadFile("web/viewer.html")
	if err != nil {
		t.Fatal(err)
	}
	want := `id="quality" type="range" min="20" max="90" value="` + strconv.Itoa(defaultQuality) + `"`
	if !strings.Contains(string(b), want) {
		t.Errorf("the quality slider does not start at the session default (%d)", defaultQuality)
	}
	if defaultQuality > 90 {
		t.Errorf("the session default (%d) is above the slider's range, so it cannot be shown", defaultQuality)
	}
}
