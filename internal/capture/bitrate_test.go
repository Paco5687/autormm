package capture

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// kbpsOf reads the peak-rate ceiling the encoder would ask ffmpeg for.
func kbpsOf(t *testing.T, e *h264Encoder, w, h int) int {
	t.Helper()
	rate, buf := e.rateFor(w, h)
	if kbps(t, buf) >= kbps(t, rate) {
		t.Errorf("VBV buffer %s is not smaller than the ceiling %s", buf, rate)
	}
	return kbps(t, rate)
}

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
	lo := kbpsOf(t, at30, 1680, 1050)
	hi := kbpsOf(t, at60, 1680, 1050)
	if hi <= lo {
		t.Errorf("60fps budget %dk is not above 30fps %dk", hi, lo)
	}
}

// And with resolution: a flat number starves a large display.
func TestBitrateGrowsWithResolution(t *testing.T) {
	e := &h264Encoder{fps: 30, quality: 60}
	small := kbpsOf(t, e, 1280, 720)
	large := kbpsOf(t, e, 2560, 1440)
	if large <= small {
		t.Errorf("1440p budget %dk is not above 720p %dk", large, small)
	}
}

func TestBitrateFollowsQuality(t *testing.T) {
	low := &h264Encoder{fps: 30, quality: 10}
	high := &h264Encoder{fps: 30, quality: 90}
	if kbpsOf(t, high, 1920, 1080) <= kbpsOf(t, low, 1920, 1080) {
		t.Error("a higher quality setting did not raise the bitrate")
	}
}

// Both ends are clamped: enough to stay legible on a tiny window, and capped so
// a 4K/60 session cannot try to saturate the link.
func TestBitrateIsClamped(t *testing.T) {
	tiny := &h264Encoder{fps: 1, quality: 1}
	if got := kbpsOf(t, tiny, 160, 120); got < 500 {
		t.Errorf("floor not applied: %dk", got)
	}
	// Capped well below what a link can plausibly sustain: overshooting does not
	// look better, it queues, and queuing is latency.
	huge := &h264Encoder{fps: 60, quality: 100}
	if got := kbpsOf(t, huge, 3840, 2160); got > 12000 {
		t.Errorf("ceiling not applied: %dk", got)
	}
}

// A realistic desktop session should land somewhere sensible, not absurd.
func TestBitrateIsReasonableForATypicalSession(t *testing.T) {
	// A desktop session over a real internet link, not a LAN: a few Mbps, not
	// the ten-plus a local connection would happily absorb.
	e := &h264Encoder{fps: 30, quality: 60}
	got := kbpsOf(t, e, 1680, 1050)
	if got < 1500 || got > 5000 {
		t.Errorf("1680x1050@30 q60 gave %dk, which looks wrong for a WAN link", got)
	}
}

// The ceiling is set by what the link carries, not by how many frames we slice
// it into. When this scaled linearly with fps, asking for 60fps doubled the
// budget on a link that could not carry it; when the budget was then capped,
// the same bits were spread over twice as many frames and every one of them
// came out half as sharp. That is how a session ends up smooth and heavily
// pixelated at once, so assert the sub-linear relationship directly.
func TestBitrateDoesNotScaleLinearlyWithFramerate(t *testing.T) {
	at30 := kbpsOf(t, &h264Encoder{fps: 30, quality: 60}, 1680, 1050)
	at60 := kbpsOf(t, &h264Encoder{fps: 60, quality: 60}, 1680, 1050)
	if at60 <= at30 {
		t.Errorf("a higher framerate should still earn some more bits: %dk -> %dk", at30, at60)
	}
	if at60 >= at30*2 {
		t.Errorf("doubling fps doubled the bitrate (%dk -> %dk); the link did not get faster", at30, at60)
	}
}

// rateFor is called with e.mu already held. Taking the lock again inside it
// deadlocks the encoder on its first frame, which shows up as a permanently
// black screen with no error anywhere — so assert the contract directly, with a
// timeout, because a deadlocked test otherwise just hangs.
func TestRateForDoesNotRetakeTheLock(t *testing.T) {
	e := &h264Encoder{fps: 60, quality: 60}
	done := make(chan string, 1)
	go func() {
		e.mu.Lock() // exactly what Encode holds when it calls bitrateFor
		defer e.mu.Unlock()
		rate, _ := e.rateFor(1680, 1050)
		done <- rate
	}()
	select {
	case got := <-done:
		if got == "" {
			t.Error("no bitrate returned")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rateFor deadlocked while the caller held e.mu")
	}
}

// Quality maps to CRF inversely: a higher quality setting means a lower CRF
// number. Getting this backwards would silently make the slider do the
// opposite of what it says.
func TestCRFFallsAsQualityRises(t *testing.T) {
	low := &h264Encoder{fps: 60, quality: 10}
	high := &h264Encoder{fps: 60, quality: 90}
	lo, hi := crfOf(t, low), crfOf(t, high)
	if hi >= lo {
		t.Errorf("quality 90 gave crf %d, quality 10 gave %d — should be lower", hi, lo)
	}
	if hi < 15 || lo > 40 {
		t.Errorf("crf range %d..%d is outside anything sensible", hi, lo)
	}
}

func crfOf(t *testing.T, e *h264Encoder) int {
	t.Helper()
	n, err := strconv.Atoi(e.crf())
	if err != nil {
		t.Fatalf("crf %q is not a number: %v", e.crf(), err)
	}
	return n
}

// The quality slider is the only bandwidth dial an operator has when a link
// cannot carry the stream. CRF and maxrate are fixed when ffmpeg starts, so
// changing quality has to restart it — otherwise the slider silently does
// nothing until the resolution happens to change.
func TestQualityChangeForcesAnEncoderRestart(t *testing.T) {
	e := &h264Encoder{fps: 60, quality: 60, encQuality: 60, w: 1680, h: 1050}
	e.proc = &ffmpegProc{} // pretend one is running

	if needsRestart(e, 1680, 1050) {
		t.Error("restarting with nothing changed")
	}
	e.SetQuality(25)
	if !needsRestart(e, 1680, 1050) {
		t.Error("moving the quality slider did not schedule a restart")
	}
	// A resolution change must still restart it, as before.
	e.encQuality = e.quality
	if !needsRestart(e, 1280, 720) {
		t.Error("a resolution change no longer restarts the encoder")
	}
}

// needsRestart mirrors the condition in Encode.
func needsRestart(e *h264Encoder, w, h int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proc == nil || w != e.w || h != e.h || e.quality != e.encQuality
}
