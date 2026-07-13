package agent

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/capture"
	"github.com/Paco5687/autormm/internal/protocol"
)

const tileSize = 128

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
	streamer := capture.NewStreamer(cptr, tileSize, ss.Quality)
	log.Printf("session %s: started (%d fps, q%d)", ss.Session, fps, ss.Quality)

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Close the media socket when the session context ends so inputLoop's
	// blocking ReadMessage unblocks on shutdown.
	go func() {
		<-ctx.Done()
		ws.Close()
	}()
	go a.frameLoop(ctx, ws, streamer, fps)
	a.inputLoop(ws, injector, streamer) // blocks until socket closes
	log.Printf("session %s: ended", ss.Session)
}

func (a *Agent) frameLoop(ctx context.Context, ws *websocket.Conn, s *capture.Streamer, fps int) {
	interval := time.Second / time.Duration(fps)
	const keyframeEvery = 4 * time.Second
	lastKey := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		force := start.Sub(lastKey) >= keyframeEvery
		data, err := s.Next(force)
		if err != nil {
			log.Printf("capture error: %v", err)
			ws.Close()
			return
		}
		if force {
			lastKey = start
		}
		if data != nil { // nil => nothing changed this tick
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.BinaryMessage, data); err != nil {
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

func (a *Agent) inputLoop(ws *websocket.Conn, in capture.Injector, s *capture.Streamer) {
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
		applyInput(ev, in, s)
	}
}

func applyInput(ev protocol.InputEvent, in capture.Injector, s *capture.Streamer) {
	switch ev.T {
	case protocol.InputSetParams:
		if ev.Quality > 0 {
			s.SetQuality(ev.Quality)
		}
		return
	}
	if in == nil {
		return // view-only session
	}
	switch ev.T {
	case protocol.InputMouseMove:
		in.MouseMove(ev.X, ev.Y)
	case protocol.InputMouseDown:
		in.MouseMove(ev.X, ev.Y)
		in.MouseButton(ev.Button, true)
	case protocol.InputMouseUp:
		in.MouseMove(ev.X, ev.Y)
		in.MouseButton(ev.Button, false)
	case protocol.InputScroll:
		in.Scroll(ev.DX, ev.DY)
	case protocol.InputKeyDown:
		in.Key(ev.Code, true)
	case protocol.InputKeyUp:
		in.Key(ev.Code, false)
	}
}

func sendErr(ws *websocket.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"t": "error", "message": msg})
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	ws.WriteMessage(websocket.TextMessage, b)
}
