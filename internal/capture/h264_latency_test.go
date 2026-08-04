package capture

import (
	"strings"
	"testing"
	"time"
)

// When the encoder falls behind, the frame worth keeping is the newest one.
// Queueing stale frames ahead of it is pure added latency — the operator waits
// to see the current screen, not the one from three frames ago.
func TestFeedKeepsOnlyTheNewestFrame(t *testing.T) {
	p := &ffmpegProc{frames: make(chan []byte, 1), done: make(chan struct{})}

	p.feed([]byte{1})
	p.feed([]byte{2})
	p.feed([]byte{3})

	got := <-p.frames
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("queued frame is %v, want the newest ([3])", got)
	}
	select {
	case extra := <-p.frames:
		t.Fatalf("a stale frame was still queued: %v", extra)
	default:
	}
}

// feed copies, so a caller reusing its buffer cannot corrupt a queued frame.
func TestFeedCopiesTheFrame(t *testing.T) {
	p := &ffmpegProc{frames: make(chan []byte, 1), done: make(chan struct{})}
	buf := []byte{9, 9}
	p.feed(buf)
	buf[0] = 0 // caller reuses its buffer
	if got := <-p.frames; got[0] != 9 {
		t.Errorf("queued frame aliased the caller's buffer: %v", got)
	}
}

// feed must never block once the process is shutting down.
func TestFeedDoesNotBlockAfterClose(t *testing.T) {
	p := &ffmpegProc{frames: make(chan []byte, 1), done: make(chan struct{})}
	p.feed([]byte{1}) // fills the queue
	close(p.done)
	done := make(chan struct{})
	go func() { p.feed([]byte{2}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feed blocked after the encoder shut down")
	}
}

// The flags that keep end-to-end latency down. Losing one silently costs
// responsiveness with nothing else to show for it, so pin them.
func TestFFmpegArgsAreLowLatency(t *testing.T) {
	args := strings.Join(ffmpegArgs(1920, 1080, 30, "4000k"), " ")
	for _, want := range []string{
		"-tune zerolatency", // no B-frames, no lookahead
		"-fflags nobuffer",  // don't buffer on the way in
		"-flags +low_delay", // nor in the codec
		"-flush_packets 1",  // emit packets as they are produced
		"-bf 0",             // B-frames would reorder, which is latency by definition
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q from: %s", want, args)
		}
	}
	// A long GOP: video and input share one TCP connection, so frequent
	// keyframes stall input behind them.
	if !strings.Contains(args, "-g 240") { // 30fps * 8
		t.Errorf("GOP is not the expected 8 seconds: %s", args)
	}
}
