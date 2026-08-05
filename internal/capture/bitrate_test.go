package capture

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func kbps(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSuffix(s, "k"))
	if err != nil {
		t.Fatalf("bitrate %q is not Nk: %v", s, err)
	}
	return n
}

// Raising the framerate must raise the budget too. The old flat bitrate meant
// asking for more frames just split the same bits between them, so a higher
// framerate looked worse rather than smoother.
func TestBitrateGrowsWithFramerate(t *testing.T) {
	at30 := &h264Encoder{fps: 30, quality: 60}
	at60 := &h264Encoder{fps: 60, quality: 60}
	lo := kbps(t, at30.bitrateFor(1680, 1050))
	hi := kbps(t, at60.bitrateFor(1680, 1050))
	if hi <= lo {
		t.Errorf("60fps budget %dk is not above 30fps %dk", hi, lo)
	}
}

// And with resolution: a flat number starves a large display.
func TestBitrateGrowsWithResolution(t *testing.T) {
	e := &h264Encoder{fps: 30, quality: 60}
	small := kbps(t, e.bitrateFor(1280, 720))
	large := kbps(t, e.bitrateFor(2560, 1440))
	if large <= small {
		t.Errorf("1440p budget %dk is not above 720p %dk", large, small)
	}
}

func TestBitrateFollowsQuality(t *testing.T) {
	low := &h264Encoder{fps: 30, quality: 10}
	high := &h264Encoder{fps: 30, quality: 90}
	if kbps(t, high.bitrateFor(1920, 1080)) <= kbps(t, low.bitrateFor(1920, 1080)) {
		t.Error("a higher quality setting did not raise the bitrate")
	}
}

// Both ends are clamped: enough to stay legible on a tiny window, and capped so
// a 4K/60 session cannot try to saturate the link.
func TestBitrateIsClamped(t *testing.T) {
	tiny := &h264Encoder{fps: 1, quality: 1}
	if got := kbps(t, tiny.bitrateFor(160, 120)); got < 1000 {
		t.Errorf("floor not applied: %dk", got)
	}
	huge := &h264Encoder{fps: 60, quality: 100}
	if got := kbps(t, huge.bitrateFor(3840, 2160)); got > 25000 {
		t.Errorf("ceiling not applied: %dk", got)
	}
}

// A realistic desktop session should land somewhere sensible, not absurd.
func TestBitrateIsReasonableForATypicalSession(t *testing.T) {
	e := &h264Encoder{fps: 60, quality: 60}
	got := kbps(t, e.bitrateFor(1680, 1050))
	if got < 2000 || got > 15000 {
		t.Errorf("1680x1050@60 q60 gave %dk, which looks wrong", got)
	}
}

// bitrateFor is called with e.mu already held. Taking the lock again inside it
// deadlocks the encoder on its first frame, which shows up as a permanently
// black screen with no error anywhere — so assert the contract directly, with a
// timeout, because a deadlocked test otherwise just hangs.
func TestBitrateForDoesNotRetakeTheLock(t *testing.T) {
	e := &h264Encoder{fps: 60, quality: 60}
	done := make(chan string, 1)
	go func() {
		e.mu.Lock() // exactly what Encode holds when it calls bitrateFor
		defer e.mu.Unlock()
		done <- e.bitrateFor(1680, 1050)
	}()
	select {
	case got := <-done:
		if got == "" {
			t.Error("no bitrate returned")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bitrateFor deadlocked while the caller held e.mu")
	}
}
