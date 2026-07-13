package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

func wsURL(httpURL, path string) string {
	return strings.Replace(httpURL, "http://", "ws://", 1) + path
}

// TestEndToEndRelay exercises the full path: agent registration, host listing,
// session creation, the server relaying a screen frame to the client and an
// input event back to the agent. No display is required.
func TestEndToEndRelay(t *testing.T) {
	srv := New(Config{
		AdminToken:   "admin-tok",
		EnrollToken:  "enroll-tok",
		SecretPhrase: "test-secret",
		OfflineAfter: 5 * time.Second,
		HistoryLen:   10,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	inputSeen := make(chan protocol.InputEvent, 1)
	go fakeAgent(t, ts.URL, "enroll-tok", inputSeen)

	if !waitFor(3*time.Second, func() bool {
		hosts := fetchHosts(t, ts.URL, "admin-tok")
		return len(hosts) == 1 && hosts[0].Online && hosts[0].CanStream
	}) {
		t.Fatal("agent did not register as online + streamable")
	}

	sess := createSession(t, ts.URL, "admin-tok", "test-host")

	cws, _, err := websocket.DefaultDialer.Dial(
		wsURL(ts.URL, "/client/session?token="+url.QueryEscape(sess.Token)), nil)
	if err != nil {
		t.Fatalf("client media dial: %v", err)
	}
	defer cws.Close()

	// Expect a screen frame to be relayed from the agent.
	cws.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := cws.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if mt != websocket.BinaryMessage {
		t.Fatalf("expected binary frame, got message type %d", mt)
	}
	codec, payload, ok := protocol.UnwrapMedia(data)
	if !ok || codec != protocol.MediaJPEGTile {
		t.Fatalf("expected JPEG-tile codec tag, got %v (ok=%v)", codec, ok)
	}
	if _, err := protocol.DecodeFrame(payload); err != nil {
		t.Fatalf("relayed frame did not decode: %v", err)
	}

	// Send an input event and confirm the agent receives it via the relay.
	if err := cws.WriteJSON(protocol.InputEvent{T: protocol.InputMouseMove, X: 42, Y: 24}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	select {
	case ev := <-inputSeen:
		if ev.T != protocol.InputMouseMove || ev.X != 42 || ev.Y != 24 {
			t.Fatalf("agent got wrong input: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent never received the relayed input event")
	}
}

// ---- fake agent ----

func fakeAgent(t *testing.T, base, enroll string, inputSeen chan protocol.InputEvent) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + enroll}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(base, "/agent/ws"), h)
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
	_ = ws.WriteJSON(protocol.Metrics{Type: protocol.TypeMetrics, CPUPercent: 12.5, MemPercent: 30})

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
			if json.Unmarshal(data, &ss) == nil {
				go fakeAgentMedia(base, ss, inputSeen)
			}
		case protocol.TypePing:
			_ = ws.WriteJSON(protocol.Pong{Type: protocol.TypePong})
		}
	}
}

func fakeAgentMedia(base string, ss protocol.StartSession, inputSeen chan protocol.InputEvent) {
	mws, _, err := websocket.DefaultDialer.Dial(
		wsURL(base, "/agent/session?token="+url.QueryEscape(ss.Token)), nil)
	if err != nil {
		return
	}
	defer mws.Close()

	frame := protocol.EncodeFrame(true, 320, 240, 128, []protocol.Tile{{TX: 0, TY: 0, JPEG: []byte("jpeg-bytes")}})
	_ = mws.WriteMessage(websocket.BinaryMessage, protocol.WrapMedia(protocol.MediaJPEGTile, frame))

	for {
		mt, data, err := mws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var ev protocol.InputEvent
		if json.Unmarshal(data, &ev) == nil {
			select {
			case inputSeen <- ev:
			default:
			}
		}
	}
}

// ---- helpers ----

func fetchHosts(t *testing.T, base, token string) []protocol.HostView {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/hosts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hosts request: %v", err)
	}
	defer res.Body.Close()
	var hosts []protocol.HostView
	_ = json.NewDecoder(res.Body).Decode(&hosts)
	return hosts
}

func createSession(t *testing.T, base, token, agentID string) protocol.SessionResponse {
	t.Helper()
	body, _ := json.Marshal(protocol.SessionRequest{AgentID: agentID, FPS: 5, Quality: 50})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/session", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("session request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session create: status %s", res.Status)
	}
	var sr protocol.SessionResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sr
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
