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

const (
	tileSize = 128
	// maxFPS bounds what a viewer may request.
	maxFPS = 60
)

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

	// Serialise all writes to the media socket (frames + cursor share it), and
	// time each one.
	//
	// The hub relays with a synchronous read-then-write loop, so when the
	// viewer's link is saturated its socket blocks, the hub stops reading, and
	// TCP pushes back to here. Time spent inside WriteMessage is therefore a
	// direct measurement of the far end of the connection — the only place the
	// bottleneck is visible, since the agent-to-hub hop is usually a LAN.
	boundSendBuffer(ws)
	link := newLinkEstimator(float64(startRateBps))
	stats := &frameStats{}
	var wmu sync.Mutex
	writeMsg := func(mt int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		t0 := time.Now()
		err := ws.WriteMessage(mt, b)
		link.observe(len(b), time.Since(t0), time.Now())
		return err
	}

	// One screen session per host. Claim the slot and stop the previous holder
	// *before* building a capturer and encoder, so the two pipelines never run
	// at once — the overlap is what would spike the host's CPU.
	closed := make(chan struct{})
	var closeOnce sync.Once
	stop := func() {
		if b, err := json.Marshal(protocol.SupersededMsg{
			T:       "superseded",
			Message: "This session was taken over by a newer connection to this host.",
		}); err == nil {
			writeMsg(websocket.TextMessage, b)
		}
		closeOnce.Do(func() { close(closed) })
	}
	if endPrevious := a.screens.claim(ss.Session, stop); endPrevious != nil {
		endPrevious()
	}
	defer a.screens.release(ss.Session)

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

	// The ceiling used to be 30 and anything above it silently fell back to 10.
	// Clamp instead, and allow more: with damage-driven encoding an idle desktop
	// costs nothing and a busy one only pays for what actually changed.
	fps := ss.FPS
	if fps <= 0 {
		fps = 30
	}
	if fps > maxFPS {
		fps = maxFPS
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
		select {
		case <-ctx.Done():
		case <-closed: // superseded by a newer session
		}
		ws.Close()
	}()

	// Tell the viewer which codecs this host can produce, and the display layout.
	activeCodec := ss.Codec
	if activeCodec == "" {
		activeCodec = protocol.CapJPEGTile
	}
	if cm, err := json.Marshal(protocol.CapsMsg{
		T: "caps", Codecs: capture.EncoderCaps(), Active: activeCodec,
	}); err == nil {
		writeMsg(websocket.TextMessage, cm)
	}
	// sendDisplays publishes the current layout. It is re-sent whenever the
	// selection or a resolution changes: without that the viewer keeps the
	// figures from session start, so its "current mode" is wrong and Fit
	// compares against a stale size and decides there is nothing to do.
	sendDisplays := func() {
		if dl, err := json.Marshal(protocol.DisplaysMsg{
			T: "displays", List: cptr.Displays(), Current: cptr.Selected(),
		}); err == nil {
			writeMsg(websocket.TextMessage, dl)
		}
	}
	sendDisplays()

	// Any resolution this session changes is put back when it ends: a host
	// shrunk to suit the operator's screen should not stay that way for whoever
	// is sitting at it.
	modes := newDisplayModeMemory()
	defer func() {
		if !modes.changed() {
			return
		}
		for index, err := range modes.restore(capture.SetDisplayMode) {
			log.Printf("session %s: could not restore display %d: %v", ss.Session, index, err)
		}
		log.Printf("session %s: restored the display mode(s) it changed", ss.Session)
	}()

	// rememberMode captures a display's current size before we change it. Read
	// from the capturer so it is the mode actually in effect, not what the
	// viewer believes.
	rememberMode := func(index int) {
		for _, d := range cptr.Displays() {
			if d.Index == index {
				modes.remember(index, d.W, d.H)
				return
			}
		}
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

	go watchLockScreen(ctx, writeMsg)
	go rateLoop(ctx, link, encoders, ss.Session, writeMsg, stats)
	go a.frameLoop(ctx, writeMsg, cptr, encoders, fps, stats)
	go a.cursorLoop(ctx, writeMsg, cursor, cptr)
	go a.clipboardLoop(ctx, writeMsg)
	a.inputLoop(ws, injector, encoders, cptr, switchCodec, sendDisplays, rememberMode, link) // blocks until socket closes
	log.Printf("session %s: ended", ss.Session)
}

func (a *Agent) frameLoop(ctx context.Context, write func(int, []byte) error, cap capture.Capturer, encoders *encHolder, fps int, stats *frameStats) {
	// Pace at exactly the rate the encoder was built for. These two must agree:
	// ffmpeg is started with -framerate fps and a bitrate budget sized for it, so
	// feeding it faster halves the bits available per frame and doubles the CPU
	// cost for a picture that looks worse. An earlier attempt to shave latency by
	// pacing event-driven capturers at maxFPS did exactly that.
	interval := time.Second / time.Duration(fps)
	const keyframeEvery = 4 * time.Second
	lastKey := time.Time{}
	captureFails := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		force := start.Sub(lastKey) >= keyframeEvery
		img, err := cap.Capture()
		capTook := time.Since(start)
		if err == capture.ErrNoChange {
			stats.addIdle()
			// Nothing was repainted. The accelerated backend already blocked
			// waiting for a change, so loop straight back rather than encoding an
			// identical picture — an idle desktop then costs almost nothing. The
			// floor is insurance: a backend that reports "no change" instantly
			// (e.g. while its duplication is being rebuilt) must not spin.
			captureFails = 0
			if spent := time.Since(start); spent < 2*time.Millisecond {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
			continue
		}
		if err != nil {
			// Capture fails transiently — a desktop switch (lock / sign-in / UAC)
			// or a mode change makes BitBlt fail for a moment. Skip the frame and
			// retry instead of ending the session, so the picture resumes on its
			// own. Force a keyframe next success so the viewer refreshes fully.
			captureFails++
			if captureFails%30 == 1 { // ~ every 3s at 10fps, not every frame
				log.Printf("capture error (skipping frame): %v", err)
			}
			lastKey = time.Time{}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			continue
		}
		captureFails = 0
		if force {
			lastKey = start
		}
		// Pass the damage the capturer reported: the JPEG-tile encoder then only
		// examines tiles that actually changed instead of hashing the whole frame.
		encStart := time.Now()
		msgs, err := encoders.get().Encode(img, force, cap.Dirty())
		encTook := time.Since(encStart)
		if err != nil {
			log.Printf("encode error: %v", err)
			return
		}
		txStart := time.Now()
		sent := 0
		for _, msg := range msgs { // codec-tagged; may be 0 (nothing changed / pipeline lag)
			if err := write(websocket.BinaryMessage, msg); err != nil {
				return
			}
			sent += len(msg)
		}
		stats.addFrame(capTook, encTook, time.Since(txStart), sent)
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

func (a *Agent) inputLoop(ws *websocket.Conn, in capture.Injector, encoders *encHolder, cptr capture.Capturer, switchCodec func(string), sendDisplays func(), rememberMode func(int), link *linkEstimator) {
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
			sendDisplays()          // the selection moved; refresh the viewer's view of it
			continue
		case protocol.InputSetCodec:
			switchCodec(ev.Codec)
			continue
		case protocol.InputClipboard:
			capture.SetClipboard(ev.Clip)
			continue
		case protocol.InputSetRes:
			if ev.Display >= 0 && ev.W > 0 && ev.H > 0 {
				rememberMode(ev.Display) // so it can be put back on disconnect
				if err := capture.SetDisplayMode(ev.Display, ev.W, ev.H); err != nil {
					log.Printf("set resolution %dx%d on display %d: %v", ev.W, ev.H, ev.Display, err)
				} else {
					cptr.Select(ev.Display) // refresh the captured region to the new size
					// Report the new geometry, or the viewer keeps offering the
					// mode it just left and Fit sees nothing to change.
					sendDisplays()
				}
			}
			continue
		case protocol.InputRxRate:
			// What the viewer actually receives, which is the one figure no
			// buffering between here and there can inflate.
			if ev.Kbps >= 0 {
				link.observeReceived(float64(ev.Kbps) * 1000)
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

// watchLockScreen tells the viewer when the host switches to a desktop this
// agent cannot capture (Windows lock / sign-in screen), so a blank stream is
// explained rather than looking like a broken connection. It keeps streaming:
// the frames resume by themselves once someone signs back in.
func watchLockScreen(ctx context.Context, writeMsg func(int, []byte) error) {
	const lockedNote = "This host is locked. Windows shows the sign-in screen on a separate secure desktop that the agent cannot capture — sign in at the machine (or via RDP) to resume the view."
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	sent := false
	for {
		locked := capture.ScreenLocked()
		if locked != sent {
			msg := ""
			if locked {
				msg = lockedNote
			}
			if b, err := json.Marshal(protocol.NoticeMsg{T: "notice", Message: msg}); err == nil {
				writeMsg(websocket.TextMessage, b)
			}
			sent = locked
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
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
