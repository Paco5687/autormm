package agent

import (
	"context"
	"encoding/json"
	"image"
	"log"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/capture"
	"github.com/Paco5687/autormm/internal/protocol"
)

const tileSize = 128

// encHolder holds the active encoder so the frame loop and input loop (which
// can swap the codec mid-session) share it safely.
type encHolder struct {
	mu  sync.Mutex
	enc capture.Encoder
}

func (h *encHolder) get() capture.Encoder {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enc
}

func (h *encHolder) swap(e capture.Encoder) capture.Encoder {
	h.mu.Lock()
	old := h.enc
	h.enc = e
	h.mu.Unlock()
	return old
}

// safeStartSession runs a session with panic recovery so a bug in any one
// session (screen, terminal, file) can never crash the whole agent -- it logs a
// stack trace and the agent keeps running.
func (a *Agent) safeStartSession(parent context.Context, ss protocol.StartSession) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("session %s panicked (recovered): %v\n%s", ss.Session, r, debug.Stack())
		}
	}()
	a.startSession(parent, ss)
}

// startSession opens the media socket for a remote-desktop session, streams
// frames, and applies incoming input events.
func (a *Agent) startSession(parent context.Context, ss protocol.StartSession) {
	mediaURL, err := a.wsURL("/agent/session", url.Values{"token": {ss.Token}})
	if err != nil {
		log.Printf("session %s: bad url: %v", ss.Session, err)
		return
	}
	ws, _, err := a.dialer.DialContext(parent, mediaURL, nil)
	if err != nil {
		log.Printf("session %s: dial failed: %v", ss.Session, err)
		return
	}
	defer ws.Close()

	if ss.Kind == protocol.SessionTerminal {
		a.runTerminal(parent, ws, ss)
		return
	}
	if ss.Kind == protocol.SessionFile {
		a.runFileSession(parent, ws)
		return
	}

	cptr, err := capture.NewCapturer()
	if err != nil {
		log.Printf("session %s: capture unavailable: %v", ss.Session, err)
		sendErr(ws, "capture unavailable: "+err.Error())
		return
	}
	defer cptr.Close()

	injector, err := capture.NewInjector()
	if err != nil {
		log.Printf("session %s: input injection unavailable (view-only): %v", ss.Session, err)
		injector = nil // stream continues without remote control
	} else {
		defer injector.Close()
	}

	fps := ss.FPS
	if fps <= 0 || fps > 30 {
		fps = 10
	}
	enc0, err := capture.NewEncoder(ss.Codec, tileSize, ss.Quality, fps)
	if err != nil {
		log.Printf("session %s: %v -- falling back to JPEG-tile", ss.Session, err)
		enc0 = capture.NewStreamer(tileSize, ss.Quality)
	}
	encoders := &encHolder{enc: enc0}
	defer func() { encoders.get().Close() }()
	cursor, cerr := capture.NewCursor() // best-effort; nil overlay if unsupported
	if cerr != nil {
		cursor = nil
	} else {
		defer cursor.Close()
	}
	log.Printf("session %s: started (%d fps, q%d)", ss.Session, fps, ss.Quality)

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Close the media socket when the session context ends so inputLoop's
	// blocking ReadMessage unblocks on shutdown.
	go func() {
		<-ctx.Done()
		ws.Close()
	}()

	// Serialise all writes to the media socket (frames + cursor share it).
	var wmu sync.Mutex
	writeMsg := func(mt int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(mt, b)
	}

	// Tell the viewer which codecs this host can produce, and the display layout.
	if cm, err := json.Marshal(protocol.CapsMsg{T: "caps", Codecs: capture.EncoderCaps()}); err == nil {
		writeMsg(websocket.TextMessage, cm)
	}
	if dl, err := json.Marshal(protocol.DisplaysMsg{T: "displays", List: cptr.Displays(), Current: -1}); err == nil {
		writeMsg(websocket.TextMessage, dl)
	}

	// Swap the encoder when the viewer requests a codec change (opt-in H.264 /
	// fall back to JPEG-tile).
	switchCodec := func(codec string) {
		ne, err := capture.NewEncoder(codec, tileSize, ss.Quality, fps)
		if err != nil {
			return
		}
		encoders.swap(ne).Close()
		log.Printf("session %s: codec -> %s", ss.Session, codec)
	}

	go a.frameLoop(ctx, writeMsg, cptr, encoders, fps)
	go a.cursorLoop(ctx, writeMsg, cursor, cptr)
	go a.clipboardLoop(ctx, writeMsg)
	a.inputLoop(ws, injector, encoders, cptr, switchCodec) // blocks until socket closes
	log.Printf("session %s: ended", ss.Session)
}

func (a *Agent) frameLoop(ctx context.Context, write func(int, []byte) error, cap capture.Capturer, encoders *encHolder, fps int) {
	interval := time.Second / time.Duration(fps)
	const keyframeEvery = 4 * time.Second
	lastKey := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		force := start.Sub(lastKey) >= keyframeEvery
		img, err := cap.Capture()
		if err != nil {
			log.Printf("capture error: %v", err)
			return
		}
		if force {
			lastKey = start
		}
		msgs, err := encoders.get().Encode(img, force)
		if err != nil {
			log.Printf("encode error: %v", err)
			return
		}
		for _, msg := range msgs { // codec-tagged; may be 0 (nothing changed / pipeline lag)
			if err := write(websocket.BinaryMessage, msg); err != nil {
				return
			}
		}
		if d := interval - time.Since(start); d > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
		}
	}
}

// cursorLoop sends the host pointer position (frame-relative) to the viewer at
// ~30 Hz, only on change, so the cursor overlay tracks smoothly regardless of
// the video frame rate.
func (a *Agent) cursorLoop(ctx context.Context, write func(int, []byte) error, cur capture.Cursor, cptr capture.Capturer) {
	if cur == nil {
		return
	}
	t := time.NewTicker(33 * time.Millisecond)
	defer t.Stop()
	var lx, ly int
	var lvis bool
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		x, y, vis, ok := cur.Pos()
		if !ok {
			continue
		}
		// Map to the currently-captured region; hide when the pointer is on a
		// display that isn't being shown.
		b := cptr.Bounds()
		if !(image.Point{X: x, Y: y}).In(b) {
			vis = false
		}
		cx, cy := x-b.Min.X, y-b.Min.Y
		if !first && cx == lx && cy == ly && vis == lvis {
			continue
		}
		first, lx, ly, lvis = false, cx, cy, vis
		msg, _ := json.Marshal(protocol.CursorMsg{T: "cursor", X: cx, Y: cy, Vis: vis})
		if write(websocket.TextMessage, msg) != nil {
			return
		}
	}
}

// clipboardLoop watches the host clipboard and pushes text changes to the viewer
// so host->viewer copy/paste works. Viewer->host goes the other way via the
// InputClipboard message. Polling keeps this cross-platform and simple.
func (a *Agent) clipboardLoop(ctx context.Context, write func(int, []byte) error) {
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	last, have := "", false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s, ok := capture.GetClipboard()
		if !ok || (have && s == last) {
			continue
		}
		last, have = s, true
		msg, _ := json.Marshal(protocol.ClipMsg{T: "clip", D: s})
		if write(websocket.TextMessage, msg) != nil {
			return
		}
	}
}

func (a *Agent) inputLoop(ws *websocket.Conn, in capture.Injector, encoders *encHolder, cptr capture.Capturer, switchCodec func(string)) {
	for {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var ev protocol.InputEvent
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.T {
		case protocol.InputDisplay:
			cptr.Select(ev.Display) // -1 all, 0..N-1 one; encoder re-keyframes on size change
			continue
		case protocol.InputSetCodec:
			switchCodec(ev.Codec)
			continue
		case protocol.InputClipboard:
			capture.SetClipboard(ev.Clip)
			continue
		case protocol.InputSetRes:
			if ev.Display >= 0 && ev.W > 0 && ev.H > 0 {
				if err := capture.SetDisplayMode(ev.Display, ev.W, ev.H); err != nil {
					log.Printf("set resolution %dx%d on display %d: %v", ev.W, ev.H, ev.Display, err)
				} else {
					cptr.Select(ev.Display) // refresh the captured region to the new size
				}
			}
			continue
		case protocol.InputSetParams:
			if ev.Quality > 0 {
				encoders.get().SetQuality(ev.Quality)
			}
			continue
		}
		applyInput(ev, in, cptr)
	}
}

func applyInput(ev protocol.InputEvent, in capture.Injector, cptr capture.Capturer) {
	if in == nil {
		return // view-only session
	}
	// Viewer coordinates are relative to the captured region; add its origin so
	// input lands on the right monitor when viewing one display or the union.
	b := cptr.Bounds()
	ax, ay := b.Min.X+ev.X, b.Min.Y+ev.Y
	switch ev.T {
	case protocol.InputMouseMove:
		in.MouseMove(ax, ay)
	case protocol.InputMouseDown:
		in.MouseMove(ax, ay)
		in.MouseButton(ev.Button, true)
	case protocol.InputMouseUp:
		in.MouseMove(ax, ay)
		in.MouseButton(ev.Button, false)
	case protocol.InputScroll:
		in.Scroll(ev.DX, ev.DY)
	case protocol.InputKeyDown:
		in.Key(ev.Code, true)
	case protocol.InputKeyUp:
		in.Key(ev.Code, false)
	case protocol.InputType:
		if err := in.TypeText(ev.Text); err != nil {
			log.Printf("TypeText error: %v", err) // don't log the text itself (privacy)
		}
	}
}

func sendErr(ws *websocket.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"t": "error", "message": msg})
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	ws.WriteMessage(websocket.TextMessage, b)
}
