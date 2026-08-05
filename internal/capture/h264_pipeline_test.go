package capture

import (
	"bytes"
	"image"
	"image/color"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// aud builds an access unit: an AUD delimiter followed by some payload. The
// splitter keys on the three-byte start code, so use that form here — with a
// four-byte one the leading zero stays attached to the preceding unit as
// (harmless) trailing padding, which only obscures what these tests are about.
func aud(payload ...byte) []byte {
	return append([]byte{0, 0, 1, 9, 0x10}, payload...)
}

// The splitter can only *prove* an access unit is finished when the next one's
// delimiter arrives, so it holds the newest unit back. readLoop releases it on
// pipe silence instead. That is only correct if flushing yields exactly the
// buffered unit and the next push does not re-emit it.
func TestSplitterIdleFlushEmitsEachUnitExactlyOnce(t *testing.T) {
	s := &auSplitter{}

	if got := s.push(aud(1, 1, 1)); got != nil {
		t.Fatalf("a lone unit must not be emitted on push (it may still be growing): %v", got)
	}
	first := s.flush() // what readLoop does when the pipe goes quiet
	if !bytes.Equal(first, aud(1, 1, 1)) {
		t.Fatalf("idle flush returned %v, want the buffered unit %v", first, aud(1, 1, 1))
	}
	if got := s.push(aud(2, 2, 2)); got != nil {
		t.Fatalf("unit re-emitted after it was already flushed: %v", got)
	}
	if second := s.flush(); !bytes.Equal(second, aud(2, 2, 2)) {
		t.Fatalf("second flush returned %v, want %v", second, aud(2, 2, 2))
	}
}

// The ordinary path still works: a chunk holding two delimiters emits the
// completed unit and keeps the rest.
func TestSplitterEmitsCompletedUnitWhenNextDelimiterArrives(t *testing.T) {
	s := &auSplitter{}
	chunk := append(aud(1, 1), aud(2, 2)...)
	got := s.push(chunk)
	if len(got) != 1 || !bytes.Equal(got[0], aud(1, 1)) {
		t.Fatalf("push returned %v, want one completed unit %v", got, aud(1, 1))
	}
	if rest := s.flush(); !bytes.Equal(rest, aud(2, 2)) {
		t.Fatalf("leftover is %v, want the partial unit %v", rest, aud(2, 2))
	}
}

func benchFrame(w, h, step int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(7)) // same every call: a static detailed desktop
	for i := range img.Pix {
		img.Pix[i] = byte(rnd.Intn(256))
	}
	for y := 700; y < 820; y++ { // one small moving window
		for x := 150 + step*8; x < 330+step*8 && x < w; x++ {
			img.Set(x, y, color.RGBA{0x30, 0x80, 0xd0, 0xff})
		}
	}
	return img
}

// End-to-end latency through the encoder, which is what an operator feels.
//
// This started at ~100ms — three frame times — and none of it was ffmpeg, which
// encodes in ~8ms. It was all pipeline mistakes: reading the output buffer
// before the frame just fed had come back, holding each access unit until the
// next one's delimiter arrived, and a stale wakeup token that silently undid the
// first fix. Each cost a whole frame, and together they made H.264 feel far less
// responsive than JPEG-tile despite compressing better.
//
// Anything at or above two frame times means one of them is back.
func TestEncoderLatencyIsUnderTwoFrames(t *testing.T) {
	const W, H, FPS, N = 1680, 1050, 30, 90
	if raceEnabled {
		t.Skip("wall-clock timing is not meaningful under -race")
	}
	if ffmpegPath() == "" {
		t.Skip("ffmpeg not installed")
	}
	enc, err := newH264Encoder(60, FPS)
	if err != nil {
		t.Skip(err)
	}
	defer enc.Close()

	// Pre-generate: building a 6.9MB frame costs tens of ms and would otherwise
	// pace the loop instead of the framerate doing it.
	pre := make([]*image.RGBA, 30)
	for i := range pre {
		pre[i] = benchFrame(W, H, i)
	}

	iv := time.Second / FPS
	fedAt := make([]time.Time, 0, N)
	type ev struct {
		at    time.Time
		count int
	}
	var evs []ev
	total := 0
	st := time.Now()
	for i := 0; i < N; i++ {
		if d := time.Until(st.Add(time.Duration(i) * iv)); d > 0 {
			time.Sleep(d)
		}
		t0 := time.Now()
		msgs, err := enc.Encode(pre[i%len(pre)], false, nil)
		if err != nil {
			t.Fatal(err)
		}
		fedAt = append(fedAt, t0)
		total += len(msgs)
		evs = append(evs, ev{time.Now(), total})
	}

	var sum time.Duration
	n := 0
	for j := 0; j < len(fedAt) && j < total; j++ {
		for _, e := range evs {
			if e.count > j {
				sum += e.at.Sub(fedAt[j])
				n++
				break
			}
		}
	}
	if n == 0 {
		t.Fatal("the encoder produced no output at all")
	}
	mean := sum / time.Duration(n)
	t.Logf("mean encode->available latency: %v over %d frames (%v per frame at %dfps)", mean, n, iv, FPS)
	if mean >= 2*iv {
		t.Errorf("latency %v is %.1f frames; a pipeline stage is buffering again", mean, float64(mean)/float64(iv))
	}
}

// readLoop releases an access unit once the pipe has been quiet, rather than
// waiting for the next delimiter. If that ever fired mid-unit it would emit a
// truncated one and the decoder would break, so decode the real output.
func TestStreamDecodesCleanly(t *testing.T) {
	const W, H, FPS, N = 1280, 800, 30, 90
	if ffmpegPath() == "" {
		t.Skip("ffmpeg not installed")
	}
	enc, err := newH264Encoder(60, FPS)
	if err != nil {
		t.Skip(err)
	}
	defer enc.Close()

	pre := make([]*image.RGBA, 30)
	for i := range pre {
		pre[i] = benchFrame(W, H, i)
	}
	var stream bytes.Buffer
	units := 0
	iv := time.Second / FPS
	st := time.Now()
	for i := 0; i < N; i++ {
		if d := time.Until(st.Add(time.Duration(i) * iv)); d > 0 {
			time.Sleep(d)
		}
		msgs, err := enc.Encode(pre[i%len(pre)], false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			kind, payload, ok := protocol.UnwrapMedia(m)
			if !ok || kind != protocol.MediaH264 {
				t.Fatalf("frame %d: output is not a tagged H.264 media message", i)
			}
			stream.Write(payload[1:]) // strip the keyframe flag byte
			units++
		}
	}
	if units == 0 {
		t.Fatal("no access units emitted")
	}
	f, err := os.CreateTemp(t.TempDir(), "*.h264")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(stream.Bytes())
	f.Close()

	out, _ := exec.Command(ffmpegPath(), "-hide_banner", "-v", "error",
		"-i", f.Name(), "-f", "null", "-").CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("the decoder rejected the stream the agent actually sends:\n%s", s)
	}
}
