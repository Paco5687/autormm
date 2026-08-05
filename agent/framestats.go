package agent

import (
	"sync"
	"time"
)

// frameStats records where each frame's time actually goes.
//
// This exists because "the framerate is low" has at least four unrelated causes
// — capture, encoding, the socket, or simply nothing changing on screen — and
// they are indistinguishable from the viewer. Two rounds of this were spent
// reasoning about which one it was; the numbers settle it in one.
//
// Everything here is per-second averages, published to the viewer alongside the
// link estimate.
type frameStats struct {
	mu sync.Mutex

	frames  int           // frames actually encoded and sent
	idle    int           // capture returned "nothing changed"
	capture time.Duration // time inside Capture()
	encode  time.Duration // time inside Encode() (includes the ffmpeg round trip)
	tx      time.Duration // time inside the socket write
	bytes   int
}

func (f *frameStats) addFrame(capture, encode, tx time.Duration, bytes int) {
	f.mu.Lock()
	f.frames++
	f.capture += capture
	f.encode += encode
	f.tx += tx
	f.bytes += bytes
	f.mu.Unlock()
}

func (f *frameStats) addIdle() {
	f.mu.Lock()
	f.idle++
	f.mu.Unlock()
}

// snapshot returns the averages since the last call and resets.
type frameSnapshot struct {
	FPS                int
	Idle               int
	CapMs, EncMs, TxMs int
	Kbps               int
}

func (f *frameStats) snapshot(window time.Duration) frameSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := f.frames
	s := frameSnapshot{Idle: f.idle}
	if secs := window.Seconds(); secs > 0 {
		s.FPS = int(float64(n)/secs + 0.5)
		s.Kbps = int(float64(f.bytes) * 8 / secs / 1000)
	}
	if n > 0 {
		ms := func(d time.Duration) int { return int(d.Milliseconds()) / n }
		s.CapMs, s.EncMs, s.TxMs = ms(f.capture), ms(f.encode), ms(f.tx)
	}
	f.frames, f.idle, f.bytes = 0, 0, 0
	f.capture, f.encode, f.tx = 0, 0, 0
	return s
}
