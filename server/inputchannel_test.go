package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// inputAgent is an agent that opens the second socket when asked, and reports
// which socket each input event arrived on.
type inputAgent struct {
	base     string
	sawInput chan string // "media" or "input", per event received
	asked    chan bool   // whether the hub asked for an input channel
}

func (a *inputAgent) run(t *testing.T, enroll string) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + enroll}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(a.base, "/agent/ws"), h)
	if err != nil {
		t.Errorf("agent control dial: %v", err)
		return
	}
	defer ws.Close()
	_ = ws.WriteJSON(protocol.Register{
		Type: protocol.TypeRegister, AgentID: "test-host", Hostname: "test-host",
		OS: "linux", Platform: "test", Arch: "amd64", AgentVersion: "test", CanStream: true,
		EncoderCaps: []string{protocol.CapJPEGTile},
	})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeStartSession:
			var ss protocol.StartSession
			if json.Unmarshal(data, &ss) != nil {
				continue
			}
			select {
			case a.asked <- ss.InputChannel:
			default:
			}
			go a.serve(ss, "")
			if ss.InputChannel {
				go a.serve(ss, inputChannel)
			}
		case protocol.TypePing:
			_ = ws.WriteJSON(protocol.Pong{Type: protocol.TypePong})
		}
	}
}

func (a *inputAgent) serve(ss protocol.StartSession, ch string) {
	u := wsURL(a.base, "/agent/session?token="+url.QueryEscape(ss.Token))
	which := "media"
	if ch != "" {
		u += "&ch=" + ch
		which = ch
	}
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	if which == "media" {
		frame := protocol.EncodeFrame(true, 320, 240, 128, []protocol.Tile{{TX: 0, TY: 0, JPEG: []byte("j")}})
		_ = ws.WriteMessage(websocket.BinaryMessage, protocol.WrapMedia(protocol.MediaJPEGTile, frame))
	}
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var ev protocol.InputEvent
		if json.Unmarshal(data, &ev) == nil && ev.T != "" && ev.T != "ping" {
			select {
			case a.sawInput <- which:
			default:
			}
		}
	}
}

func inputTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv := New(Config{
		AdminToken: "admin-tok", EnrollToken: "enroll-tok", SecretPhrase: "test-secret",
		OfflineAfter: 5 * time.Second, HistoryLen: 10,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

// A keystroke must not wait behind a frame. Input and video share a TCP
// connection otherwise, and TCP will not let the click overtake the video
// queued ahead of it — so the lag tracks how busy the screen is rather than how
// far away the host is.
func TestInputTravelsOnItsOwnSocket(t *testing.T) {
	_, base := inputTestServer(t)
	ag := &inputAgent{base: base, sawInput: make(chan string, 8), asked: make(chan bool, 1)}
	go ag.run(t, "enroll-tok")

	if !waitFor(3*time.Second, func() bool {
		hosts := fetchHosts(t, base, "admin-tok")
		return len(hosts) == 1 && hosts[0].Online && hosts[0].CanStream
	}) {
		t.Fatal("agent did not register")
	}
	sess := createSession(t, base, "admin-tok", "test-host")
	u := wsURL(base, "/client/session?token="+url.QueryEscape(sess.Token))

	media, _, err := websocket.DefaultDialer.Dial(u+"&input=1", nil)
	if err != nil {
		t.Fatalf("media dial: %v", err)
	}
	defer media.Close()

	input, _, err := websocket.DefaultDialer.Dial(u+"&ch=input", nil)
	if err != nil {
		t.Fatalf("input dial: %v", err)
	}
	defer input.Close()

	// The viewer is told, before anything else, that the far end is attached.
	// Without that it cannot tell a channel the agent joined from one it never
	// did, and the difference is whether keystrokes arrive or vanish.
	input.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := input.ReadMessage()
	if err != nil {
		t.Fatalf("no word from the hub on the input socket: %v", err)
	}
	var ready protocol.InputReadyMsg
	if json.Unmarshal(data, &ready) != nil || ready.T != "inputready" {
		t.Fatalf("first message on the input socket was %q", data)
	}
	if asked := <-ag.asked; !asked {
		t.Error("the agent was never asked for an input channel")
	}

	if err := input.WriteJSON(protocol.InputEvent{T: protocol.InputKeyDown, Code: "KeyA"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	select {
	case which := <-ag.sawInput:
		if which != inputChannel {
			t.Errorf("the keystroke arrived on the %s socket", which)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the keystroke never reached the agent")
	}
}

// An agent that predates the input channel never attaches to it, and the viewer
// must go on working — it keeps sending input over the media socket, which is
// what it did before.
func TestInputStillWorksWhenTheAgentHasNoInputChannel(t *testing.T) {
	_, base := inputTestServer(t)
	inputSeen := make(chan protocol.InputEvent, 1)
	go fakeAgent(t, base, "enroll-tok", inputSeen) // the old agent: media socket only

	if !waitFor(3*time.Second, func() bool {
		hosts := fetchHosts(t, base, "admin-tok")
		return len(hosts) == 1 && hosts[0].Online && hosts[0].CanStream
	}) {
		t.Fatal("agent did not register")
	}
	sess := createSession(t, base, "admin-tok", "test-host")
	u := wsURL(base, "/client/session?token="+url.QueryEscape(sess.Token))

	media, _, err := websocket.DefaultDialer.Dial(u+"&input=1", nil)
	if err != nil {
		t.Fatalf("media dial: %v", err)
	}
	defer media.Close()
	input, _, err := websocket.DefaultDialer.Dial(u+"&ch=input", nil)
	if err != nil {
		t.Fatalf("input dial: %v", err)
	}
	defer input.Close()

	// Nothing is said on the input socket, which is the signal not to use it.
	input.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, _, err := input.ReadMessage(); err == nil {
		t.Fatal("the hub declared an input channel the agent never joined")
	}

	// And the media socket carries input exactly as before.
	media.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := media.ReadMessage(); err != nil {
		t.Fatalf("no frame on the media socket: %v", err)
	}
	if err := media.WriteJSON(protocol.InputEvent{T: protocol.InputKeyDown, Code: "KeyB"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-inputSeen:
		if ev.Code != "KeyB" {
			t.Errorf("agent saw %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input over the media socket stopped working")
	}
}

// A viewer that does not ask for one must not have the agent open it: an older
// dashboard against a new agent should behave exactly as it did.
func TestNoInputChannelUnlessTheViewerAsks(t *testing.T) {
	_, base := inputTestServer(t)
	ag := &inputAgent{base: base, sawInput: make(chan string, 8), asked: make(chan bool, 1)}
	go ag.run(t, "enroll-tok")
	if !waitFor(3*time.Second, func() bool {
		hosts := fetchHosts(t, base, "admin-tok")
		return len(hosts) == 1 && hosts[0].Online
	}) {
		t.Fatal("agent did not register")
	}
	sess := createSession(t, base, "admin-tok", "test-host")
	media, _, err := websocket.DefaultDialer.Dial(
		wsURL(base, "/client/session?token="+url.QueryEscape(sess.Token)), nil)
	if err != nil {
		t.Fatalf("media dial: %v", err)
	}
	defer media.Close()
	select {
	case asked := <-ag.asked:
		if asked {
			t.Error("an input channel was requested for a viewer that never asked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session never started")
	}
}
