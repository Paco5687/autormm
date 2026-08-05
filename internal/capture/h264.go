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
	"sync/atomic"
	"time"

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
	// encQuality is the quality the running ffmpeg was started with. When the
	// operator moves the slider these diverge and the encoder is restarted —
	// otherwise the only bandwidth dial they have does nothing until the
	// resolution happens to change.
	encQuality int
	// rateCeiling is what the viewer's link was measured to carry, in bits per
	// second; encRate is what the running ffmpeg was started with, and lastStart
	// when. Restarting costs a keyframe, so the two are only reconciled when the
	// measurement has moved materially and has been stable for a while.
	rateCeiling int
	encRate     int
	lastStart   time.Time
	w, h        int
	proc        *ffmpegProc

	outMu sync.Mutex
	out   [][]byte
	// outSig is poked whenever an access unit lands. Encode waits on it briefly
	// so it can return the frame it just fed instead of the one before.
	outSig chan struct{}
}

func newH264Encoder(quality, fps int) (Encoder, error) {
	if ffmpegPath() == "" {
		return nil, fmt.Errorf("ffmpeg not found on PATH")
	}
	if fps <= 0 {
		fps = 12
	}
	return &h264Encoder{fps: fps, quality: clampQ(quality), outSig: make(chan struct{}, 1)}, nil
}

// SetQuality changes the constant-quality target and the rate ceiling. It takes
// effect on the next frame: the encoder is restarted, because CRF and maxrate
// are fixed when ffmpeg starts. That costs one keyframe, which is worth it —
// this is the only dial an operator has when a link cannot carry the stream.
func (e *h264Encoder) SetQuality(q int) {
	e.mu.Lock()
	e.quality = clampQ(q)
	e.mu.Unlock()
}

// SetRateCeiling records what the link was measured to carry. It does not
// restart the encoder by itself — Encode reconciles it when the change is worth
// a keyframe.
func (e *h264Encoder) SetRateCeiling(bps int) {
	e.mu.Lock()
	e.rateCeiling = bps
	e.mu.Unlock()
}

// rateHold is the minimum time between restarts driven by the rate estimate.
// Each one costs a keyframe, so reacting to every wobble would trade the very
// smoothness the estimate is meant to buy.
const rateHold = 12 * time.Second

// rateMoved reports whether the measured link rate has diverged far enough from
// what ffmpeg is running with to be worth restarting for. The thresholds are
// deliberately wide: a 20% error in the ceiling is barely visible, while a
// keyframe is.
//
// The caller must already hold e.mu.
func (e *h264Encoder) rateMoved(w, h int, now time.Time) bool {
	if e.encRate <= 0 || e.rateCeiling <= 0 {
		return false
	}
	if now.Sub(e.lastStart) < rateHold {
		return false
	}
	r := float64(e.targetRate(w, h)) / float64(e.encRate)
	return r >= 1.35 || r <= 0.74
}

// Dirty regions are ignored: x264 does its own motion estimation over the
// full frame, and feeding it partial images would corrupt the reference chain.
func (e *h264Encoder) Encode(img *image.RGBA, _ bool, _ []image.Rectangle) ([][]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	e.mu.Lock()
	now := time.Now()
	if e.proc == nil || w != e.w || h != e.h || e.quality != e.encQuality || e.rateMoved(w, h, now) {
		if e.proc != nil {
			e.proc.close()
		}
		rate, buf := e.rateFor(w, h)
		p, err := startFFmpeg(ffmpegPath(), w, h, e.fps, e.crf(), rate, buf)
		if err != nil {
			e.mu.Unlock()
			return nil, err
		}
		e.proc, e.w, e.h, e.encQuality = p, w, h, e.quality
		e.encRate, e.lastStart = e.targetRate(w, h), now
		go e.readLoop(p)
	}
	p := e.proc
	e.mu.Unlock()

	// Clear any leftover wakeup before feeding, so the wait below tracks *this*
	// frame. A stale token makes waitForOutput return instantly and hand back
	// the previous frame's access unit — reintroducing exactly the frame of
	// latency the wait exists to remove, and silently.
	select {
	case <-e.outSig:
	default:
	}

	p.feed(packRGBA(img))

	// Wait briefly for the access unit belonging to the frame just fed.
	//
	// ffmpeg emits it a few milliseconds after the write, so reading the output
	// buffer immediately returns the *previous* frame's AU and leaves this one to
	// be picked up by the next call — a whole frame of latency (33ms at 30fps)
	// bought for the sake of a few milliseconds' impatience. Measured end to end,
	// that mistake was two thirds of the encoder's ~100ms delay, and it is why
	// H.264 felt markedly less responsive than JPEG-tile despite compressing far
	// better.
	//
	// The wait is bounded well inside a frame interval: if the encoder is running
	// behind, returning late is worse than returning what is ready.
	e.waitForOutput()

	e.outMu.Lock()
	msgs := e.out
	e.out = nil
	e.outMu.Unlock()
	return msgs, nil
}

// waitForOutput blocks until an access unit arrives or a fraction of a frame
// interval passes, whichever comes first.
func (e *h264Encoder) waitForOutput() {
	wait := time.Second / time.Duration(2*e.fps) // half a frame
	if wait > 15*time.Millisecond {
		wait = 15 * time.Millisecond
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-e.outSig:
	case <-t.C:
	}
}

// settled is how long the pipe must be quiet before a buffered access unit is
// treated as complete.
//
// The splitter delimits on AUD NALs, so an AU is only *provably* finished when
// the next one's delimiter arrives — a whole frame later, 33ms at 30fps, for a
// unit that was ready immediately. Measured over a 150-frame session, gaps
// between successive reads inside a single AU never exceeded 32µs, while gaps
// between AUs were never shorter than 14ms. 3ms sits two orders of magnitude
// above the first and well below the second, so silence is a safe completeness
// signal and the frame of delay disappears.
const settled = 3 * time.Millisecond

func (e *h264Encoder) readLoop(p *ffmpegProc) {
	spl := &auSplitter{}
	chunks := make(chan []byte, 16)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 64*1024)
			n, err := p.stdout.Read(buf)
			if n > 0 {
				chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()

	idle := time.NewTimer(time.Hour)
	idle.Stop()
	defer idle.Stop()
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				if au := spl.flush(); au != nil {
					e.appendAU(au)
				}
				return
			}
			for _, au := range spl.push(c) {
				e.appendAU(au)
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(settled)
		case <-idle.C:
			// The pipe went quiet: whatever the splitter is holding is a
			// complete access unit, so send it now instead of next frame.
			if au := spl.flush(); au != nil {
				e.appendAU(au)
			}
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
	select { // wake a waiting Encode; never block the reader
	case e.outSig <- struct{}{}:
	default:
	}
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

// crf picks the constant-quality target. Lower is better quality and more
// bits. The range suits desktop content, which compresses far harder than
// video: text stays crisp well into the high twenties.
//
// The caller must already hold e.mu.
func (e *h264Encoder) crf() string {
	// Tuned for a link crossing the internet rather than a LAN. Screen text
	// survives high CRF far better than video does, and bandwidth is the scarce
	// resource here: a picture that arrives late is worse than one slightly
	// softer.
	const best, worst = 20.0, 40.0
	v := worst - (worst-best)*float64(clampQ(e.quality))/100
	return strconv.Itoa(int(v + 0.5))
}

// rateFor returns the peak bitrate and VBV buffer for a stream of this size.
//
// This is a ceiling, not a target: with -crf the encoder spends what the
// picture needs and nothing more, so an idle desktop is nearly free and the cap
// only bites during heavy motion. Sending more than the link carries does not
// look better, it just queues — and queuing is latency.
//
// The buffer is deliberately small. A large VBV smooths bitrate by delaying
// frames, which is precisely the wrong trade here.
//
// The caller must already hold e.mu — Encode does, and taking it again here
// deadlocks on the very first frame, which presents as a permanently black
// screen rather than as an error.
func (e *h264Encoder) rateFor(w, h int) (maxrate, bufsize string) {
	kb := e.targetRate(w, h) / 1000
	// ~500ms of buffer. A quarter-second was too tight to absorb a scroll or a
	// window drag without the picture falling apart; intra-refresh removes the
	// keyframe spike that made a small buffer seem necessary.
	return strconv.Itoa(kb) + "k", strconv.Itoa(kb/2) + "k"
}

// targetRate is the peak bitrate to run at, in bits per second.
//
// When the link has been measured, that measurement wins outright. It is not
// combined with the quality setting, because the two answer different
// questions: quality (via CRF) is how sharp the operator wants the picture,
// while this is how much the connection can actually deliver. Measured on a
// window drag at 1680x1050, CRF 28 wants ~21Mbps; the old hand-picked 3.6Mbps
// cap held it to a sixth of that and cost 6.4dB, which is what "blocky during
// motion" looks like. On a slower link the same fixed number is too high and
// the excess merely queues. Only measurement fits both.
//
// Before any measurement exists — the first seconds of a session — fall back to
// a rate derived from quality and resolution.
//
// The caller must already hold e.mu.
func (e *h264Encoder) targetRate(w, h int) int {
	if e.rateCeiling > 0 {
		return clampBps(float64(e.rateCeiling))
	}

	// Peak bits per pixel during motion, at the reference framerate. Quality
	// picks a point in the range.
	const minBPP, maxBPP = 0.02, 0.10
	bpp := minBPP + (maxBPP-minBPP)*float64(clampQ(e.quality))/100

	// The ceiling is set by what the *link* carries, not by how many frames we
	// choose to divide it into, so it must not scale with fps the way a naive
	// pixel-rate calculation does. Doubling the framerate on a fixed link only
	// halves the bits in every frame — which is how a session ends up smooth
	// and heavily pixelated at the same time. Above the reference rate, allow
	// some extra but nothing like double.
	const refFPS = 30.0
	f := float64(e.fps)
	if f > refFPS {
		f = refFPS + (f-refFPS)*0.5
	}
	return clampBps(float64(w) * float64(h) * f * bpp)
}

func clampBps(bps float64) int {
	const minBps, maxBps = 500_000.0, 25_000_000.0
	if bps < minBps {
		bps = minBps
	}
	if bps > maxBps {
		bps = maxBps
	}
	return int(bps)
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
	writing   atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

// ffmpegArgs builds the encoder command line. Split out from startFFmpeg so the
// latency-critical flags can be asserted without running ffmpeg.
func ffmpegArgs(w, h, fps int, crf, bitrate, bufsize string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		// Don't let the input layer buffer: every frame should go straight to
		// the encoder.
		"-fflags", "nobuffer",
		"-f", "rawvideo", "-pixel_format", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h), "-framerate", strconv.Itoa(fps), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-flags", "+low_delay",
		// Periodic intra *refresh* rather than periodic keyframes. A full IDR at
		// desktop resolutions wants several hundred KB, which no low-latency VBV
		// buffer can hold, so x264 crushes it to fit: the keyframe lands visibly
		// blocky and the following P-frames spend seconds repairing it. That is
		// the "wave of pixels" every few seconds. Worse, the burst shares one TCP
		// connection with input, so the operator's clicks queue behind it and
		// responsiveness dips in time with the wave.
		//
		// intra-refresh spreads the same intra blocks across the whole period as
		// a moving column, so every frame costs about the same and neither defect
		// happens. x264 still emits one real IDR as the first frame, which is
		// what the decoder keys on; the stream is reliable, so nothing later
		// needs another one.
		// Constant quality with a hard ceiling, not a target average. -b:v made
		// x264 spend its whole budget every second even on a motionless desktop;
		// screen content should cost almost nothing when nothing moves, and the
		// bits should go where the motion is. maxrate/bufsize keep the peak
		// inside what the link can carry, with a small buffer because a large
		// one is just latency waiting to happen.
		"-crf", crf, "-maxrate", bitrate, "-bufsize", bufsize,
		"-g", strconv.Itoa(fps * 8), "-bf", "0",
		// vbv-init=1.0 starts the buffer full so the one unavoidable IDR — the
		// first frame of the session — is not squeezed. It costs nothing after
		// startup, and it is what the operator sees the instant they connect.
		"-x264-params", "repeat-headers=1:aud=1:intra-refresh=1:vbv-init=1.0",
		// Emit each packet as soon as it exists instead of batching.
		"-flush_packets", "1",
		"-f", "h264", "pipe:1",
	}
}

func startFFmpeg(bin string, w, h, fps int, crf, bitrate, bufsize string) (*ffmpegProc, error) {
	cmd := exec.Command(bin, ffmpegArgs(w, h, fps, crf, bitrate, bufsize)...)
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
	p := &ffmpegProc{stdin: stdin, stdout: stdout, cmd: cmd, done: make(chan struct{})}
	return p, nil
}

// feed queues a copy of the frame, dropping it if ffmpeg is behind.
// feed hands a frame to ffmpeg, keeping only the newest one queued.
//
// The queue used to hold three frames and drop the *newest* when full, which is
// backwards: at 30fps that parked up to 100ms of stale frames ahead of the one
// the operator is waiting to see. When the encoder falls behind, the right frame
// to keep is always the latest.
// feed hands a raw frame to ffmpeg, writing it straight down the pipe.
//
// This used to go through a one-deep channel and a writer goroutine, which cost
// a whole frame of latency: the frame sat in the channel while the goroutine
// finished the previous write, so ffmpeg saw it a frame-time late. The write
// itself measures ~2ms for a 6.9MB frame (the pipe sustains several GB/s), so
// there was nothing to gain by making it asynchronous.
//
// Dropping is preserved: if a write really is still in flight, this frame is
// discarded rather than queued. A stale frame helps nobody, and queueing is the
// thing being removed.
func (p *ffmpegProc) feed(frame []byte) {
	select {
	case <-p.done:
		return
	default:
	}
	if !p.writing.CompareAndSwap(false, true) {
		return
	}
	defer p.writing.Store(false)
	p.stdin.Write(frame)
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
