package capture

import (
	"strings"
	"testing"
	"time"
)

// blockingWriter holds a write open until released, so a test can keep one
// "in flight" and observe what a concurrent feed does.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.started <- struct{}{}
	<-b.release
	return len(p), nil
}
func (b *blockingWriter) Close() error { return nil }

// When the encoder falls behind, the frame worth keeping is the newest one.
// Queueing stale frames ahead of it is pure added latency — the operator waits
// to see the current screen, not the one from three frames ago. feed therefore
// drops rather than queues, and never blocks the capture loop behind a write.
func TestFeedDropsRatherThanQueueing(t *testing.T) {
	bw := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	p := &ffmpegProc{stdin: bw, done: make(chan struct{})}

	go p.feed([]byte{1})
	<-bw.started // a write is now in flight

	done := make(chan struct{})
	go func() { p.feed([]byte{2}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feed blocked behind an in-flight write instead of dropping the frame")
	}
	close(bw.release)
}

// feed must never block once the process is shutting down.
func TestFeedDoesNotBlockAfterClose(t *testing.T) {
	bw := &blockingWriter{started: make(chan struct{}, 1), release: make(chan struct{})}
	close(bw.release)
	p := &ffmpegProc{stdin: bw, done: make(chan struct{})}
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
	args := strings.Join(ffmpegArgs(1920, 1080, 30, "24", "4000k", "1000k"), " ")
	for _, want := range []string{
		"-tune zerolatency", // no B-frames, no lookahead
		"-fflags nobuffer",  // don't buffer on the way in
		"-flags +low_delay", // nor in the codec
		"-flush_packets 1",  // emit packets as they are produced
		"-bf 0",             // B-frames would reorder, which is latency by definition
		"-crf 24",           // constant quality...
		"-maxrate 4000k",    // ...with a hard ceiling, not a target average
		"-bufsize 1000k",    // small VBV: a big one buys smoothness with latency
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

// Periodic IDR frames are the single worst thing that can happen to this
// stream. A full keyframe at desktop resolution wants several hundred KB, which
// no low-latency VBV buffer can hold, so x264 crushes it to fit rather than
// exceed the ceiling. Measured on static desktop content, that is a ~20dB PSNR
// collapse at every GOP boundary — the picture visibly shatters and reassembles
// every few seconds, and input queues behind the burst on the shared
// connection. intra-refresh spreads the same intra blocks across the period
// instead, which measured a 0.4dB dip in the same test.
//
// Dropping this one parameter brings the whole defect back with nothing to
// indicate why, so pin it.
func TestFFmpegArgsUseIntraRefreshNotPeriodicKeyframes(t *testing.T) {
	args := strings.Join(ffmpegArgs(1920, 1080, 30, "28", "3600k", "1800k"), " ")
	if !strings.Contains(args, "intra-refresh=1") {
		t.Errorf("intra-refresh is off, so every GOP boundary is a crushed keyframe: %s", args)
	}
	// The first frame is the one IDR the stream cannot avoid, and it is the
	// first thing the operator sees. Starting the VBV full keeps it from being
	// squeezed; it costs nothing once the session is running.
	if !strings.Contains(args, "vbv-init=1.0") {
		t.Errorf("VBV starts empty, so the session opens on a crushed frame: %s", args)
	}
}
