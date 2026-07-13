package capture

import (
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"sync"

	"github.com/Paco5687/autormm/internal/protocol"
)

var (
	ffmpegOnce sync.Once
	ffmpegBin  string
)

// ffmpegPath returns the ffmpeg binary path (cached), or "" if not installed.
func ffmpegPath() string {
	ffmpegOnce.Do(func() { ffmpegBin, _ = exec.LookPath("ffmpeg") })
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

func (e *h264Encoder) Encode(img *image.RGBA, _ bool) ([][]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	e.mu.Lock()
	if e.proc == nil || w != e.w || h != e.h {
		if e.proc != nil {
			e.proc.close()
		}
		p, err := startFFmpeg(ffmpegPath(), w, h, e.fps, e.bitrate())
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

func (e *h264Encoder) bitrate() string {
	// Map quality 1-100 to roughly 570 kbps .. 7.5 Mbps.
	return strconv.Itoa(500+e.quality*70) + "k"
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

func startFFmpeg(bin string, w, h, fps int, bitrate string) (*ffmpegProc, error) {
	cmd := exec.Command(bin,
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pixel_format", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h), "-framerate", strconv.Itoa(fps), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-b:v", bitrate, "-g", strconv.Itoa(fps*2), "-bf", "0",
		"-x264-params", "repeat-headers=1:aud=1",
		"-f", "h264", "pipe:1",
	)
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
	p := &ffmpegProc{stdin: stdin, stdout: stdout, cmd: cmd, frames: make(chan []byte, 3), done: make(chan struct{})}
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
func (p *ffmpegProc) feed(frame []byte) {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	select {
	case p.frames <- cp:
	case <-p.done:
	default: // ffmpeg behind — drop
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
