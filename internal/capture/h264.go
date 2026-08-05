package capture

import (
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/Paco5687/autormm/internal/protocol"
)

var (
	ffmpegMu  sync.Mutex
	ffmpegBin string
)

// ffmpegPath returns the ffmpeg binary, or "" if it is not installed.
//
// A copy sitting beside the agent binary wins over PATH: the hub can install a
// private one there without touching the host's PATH or needing a package
// manager. A *negative* result is deliberately never cached, so a host that
// gains ffmpeg later advertises H.264 on its next session instead of having to
// be restarted — the viewer's codec list comes from the per-session caps
// message, which is recomputed each time.
func ffmpegPath() string {
	ffmpegMu.Lock()
	defer ffmpegMu.Unlock()

	if ffmpegBin != "" {
		if _, err := os.Stat(ffmpegBin); err == nil {
			return ffmpegBin
		}
		ffmpegBin = "" // it was removed; look again
	}

	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), name)
		if fi, err := os.Stat(beside); err == nil && !fi.IsDir() {
			ffmpegBin = beside
			return ffmpegBin
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpegBin = p
	}
	return ffmpegBin
}

// h264Encoder pipes raw frames through ffmpeg/libx264 and emits per-frame H.264
// access units (Annex-B) tagged as MediaH264 for the browser's WebCodecs decoder.
// The pipeline is asynchronous, so Encode may return AUs for earlier frames.
type h264Encoder struct {
	fps int

	mu      sync.Mutex
	quality int
	w, h    int
	proc    *ffmpegProc

	outMu sync.Mutex
	out   [][]byte
}

func newH264Encoder(quality, fps int) (Encoder, error) {
	if ffmpegPath() == "" {
		return nil, fmt.Errorf("ffmpeg not found on PATH")
	}
	if fps <= 0 {
		fps = 12
	}
	return &h264Encoder{fps: fps, quality: clampQ(quality)}, nil
}

func (e *h264Encoder) SetQuality(q int) {
	e.mu.Lock()
	e.quality = clampQ(q)
	e.mu.Unlock()
	// Takes effect when ffmpeg (re)starts, e.g. on the next display/size change.
}

// Dirty regions are ignored: x264 does its own motion estimation over the
// full frame, and feeding it partial images would corrupt the reference chain.
func (e *h264Encoder) Encode(img *image.RGBA, _ bool, _ []image.Rectangle) ([][]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	e.mu.Lock()
	if e.proc == nil || w != e.w || h != e.h {
		if e.proc != nil {
			e.proc.close()
		}
		p, err := startFFmpeg(ffmpegPath(), w, h, e.fps, e.bitrateFor(w, h))
		if err != nil {
			e.mu.Unlock()
			return nil, err
		}
		e.proc, e.w, e.h = p, w, h
		go e.readLoop(p)
	}
	p := e.proc
	e.mu.Unlock()

	p.feed(packRGBA(img))

	e.outMu.Lock()
	msgs := e.out
	e.out = nil
	e.outMu.Unlock()
	return msgs, nil
}

func (e *h264Encoder) readLoop(p *ffmpegProc) {
	spl := &auSplitter{}
	buf := make([]byte, 64*1024)
	for {
		n, err := p.stdout.Read(buf)
		if n > 0 {
			for _, au := range spl.push(buf[:n]) {
				e.appendAU(au)
			}
		}
		if err != nil {
			if au := spl.flush(); au != nil {
				e.appendAU(au)
			}
			return
		}
	}
}

func (e *h264Encoder) appendAU(au []byte) {
	flags := byte(0)
	if auIsKeyframe(au) {
		flags = 1
	}
	msg := protocol.WrapMedia(protocol.MediaH264, append([]byte{flags}, au...))
	e.outMu.Lock()
	e.out = append(e.out, msg)
	e.outMu.Unlock()
}

func (e *h264Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proc != nil {
		e.proc.close()
		e.proc = nil
	}
	return nil
}

// bitrateFor sizes the stream to the pixel rate it actually has to carry.
//
// The old value was a flat number derived from the quality slider alone. That
// starved a large display and, worse, silently halved the bits available per
// frame whenever the framerate went up — so asking for a higher framerate made
// the picture worse instead of smoother, which is the opposite of the point.
func (e *h264Encoder) bitrateFor(w, h int) string {
	// Bits per pixel for desktop content at x264 ultrafast. Quality picks a
	// point in this range; screen content compresses far better than video, so
	// these are deliberately modest.
	const minBPP, maxBPP = 0.02, 0.12
	e.mu.Lock()
	q := e.quality
	e.mu.Unlock()

	bpp := minBPP + (maxBPP-minBPP)*float64(clampQ(q))/100
	bps := float64(w) * float64(h) * float64(e.fps) * bpp

	// Keep it sane at both ends: enough to be legible on a small window, capped
	// so a 4K/60 session cannot try to saturate the link.
	const minBps, maxBps = 1_000_000.0, 25_000_000.0
	if bps < minBps {
		bps = minBps
	}
	if bps > maxBps {
		bps = maxBps
	}
	return strconv.Itoa(int(bps/1000)) + "k"
}

func clampQ(q int) int {
	if q < 1 {
		return 1
	}
	if q > 100 {
		return 100
	}
	return q
}

// ---- ffmpeg process ----

type ffmpegProc struct {
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	cmd       *exec.Cmd
	frames    chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// ffmpegArgs builds the encoder command line. Split out from startFFmpeg so the
// latency-critical flags can be asserted without running ffmpeg.
func ffmpegArgs(w, h, fps int, bitrate string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		// Don't let the input layer buffer: every frame should go straight to
		// the encoder.
		"-fflags", "nobuffer",
		"-f", "rawvideo", "-pixel_format", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h), "-framerate", strconv.Itoa(fps), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-flags", "+low_delay",
		// A long GOP on purpose. Video and input share one TCP connection, so a
		// periodic multi-hundred-KB keyframe stalls everything queued behind it,
		// including the click the operator just made. The stream is reliable, so
		// frequent keyframes buy nothing.
		"-b:v", bitrate, "-g", strconv.Itoa(fps * 8), "-bf", "0",
		"-x264-params", "repeat-headers=1:aud=1",
		// Emit each packet as soon as it exists instead of batching.
		"-flush_packets", "1",
		"-f", "h264", "pipe:1",
	}
}

func startFFmpeg(bin string, w, h, fps int, bitrate string) (*ffmpegProc, error) {
	cmd := exec.Command(bin, ffmpegArgs(w, h, fps, bitrate)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &ffmpegProc{stdin: stdin, stdout: stdout, cmd: cmd, frames: make(chan []byte, 1), done: make(chan struct{})}
	go func() {
		for {
			select {
			case f := <-p.frames:
				if _, err := p.stdin.Write(f); err != nil {
					return
				}
			case <-p.done:
				return
			}
		}
	}()
	return p, nil
}

// feed queues a copy of the frame, dropping it if ffmpeg is behind.
// feed hands a frame to ffmpeg, keeping only the newest one queued.
//
// The queue used to hold three frames and drop the *newest* when full, which is
// backwards: at 30fps that parked up to 100ms of stale frames ahead of the one
// the operator is waiting to see. When the encoder falls behind, the right frame
// to keep is always the latest.
func (p *ffmpegProc) feed(frame []byte) {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	select {
	case p.frames <- cp:
		return
	case <-p.done:
		return
	default:
	}
	// Full: discard the stale frame sitting there and take its place.
	select {
	case <-p.frames:
	case <-p.done:
		return
	default:
	}
	select {
	case p.frames <- cp:
	case <-p.done:
	default: // the encoder goroutine grabbed it first; nothing to do
	}
}

func (p *ffmpegProc) close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.stdin.Close()
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
		p.cmd.Wait()
	})
}

// packRGBA returns tightly-packed RGBA bytes (ffmpeg rawvideo has no stride pad).
func packRGBA(img *image.RGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if img.Stride == 4*w {
		return img.Pix[:4*w*h]
	}
	out := make([]byte, 4*w*h)
	for y := 0; y < h; y++ {
		copy(out[y*4*w:(y+1)*4*w], img.Pix[y*img.Stride:y*img.Stride+4*w])
	}
	return out
}

// ---- Annex-B access-unit splitting ----

// auSplitter accumulates the H.264 elementary stream and emits complete access
// units, delimited by AUD NALs (nal type 9, enabled via x264 aud=1).
type auSplitter struct {
	buf []byte
}

func (s *auSplitter) push(data []byte) [][]byte {
	s.buf = append(s.buf, data...)
	idx := audPositions(s.buf)
	if len(idx) < 2 {
		return nil
	}
	var aus [][]byte
	for i := 0; i < len(idx)-1; i++ {
		aus = append(aus, dup(s.buf[idx[i]:idx[i+1]]))
	}
	s.buf = append([]byte(nil), s.buf[idx[len(idx)-1]:]...)
	return aus
}

func (s *auSplitter) flush() []byte {
	if len(s.buf) == 0 {
		return nil
	}
	out := s.buf
	s.buf = nil
	return out
}

// audPositions returns the offsets of AUD (access-unit delimiter) start codes.
func audPositions(b []byte) []int {
	var pos []int
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 && (b[i+3]&0x1f) == 9 {
			pos = append(pos, i)
		}
	}
	return pos
}

// auIsKeyframe reports whether an access unit contains an IDR (5) or SPS (7) NAL.
func auIsKeyframe(au []byte) bool {
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 {
			if t := au[i+3] & 0x1f; t == 5 || t == 7 {
				return true
			}
		}
	}
	return false
}

func dup(b []byte) []byte { return append([]byte(nil), b...) }
