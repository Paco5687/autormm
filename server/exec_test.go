package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestExecRoundTrip(t *testing.T) {
	srv := New(Config{AdminToken: "admin", EnrollToken: "enroll", SecretPhrase: "s"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	go fakeExecAgent(t, ts.URL, "enroll")

	if !waitFor(3*time.Second, func() bool {
		hs := fetchHosts(t, ts.URL, "admin")
		return len(hs) == 1 && hs[0].Online && hs[0].CanExec
	}) {
		t.Fatal("exec-capable agent did not register")
	}

	body, _ := json.Marshal(map[string]any{"agent_id": "exec-host", "command": "echo hi"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/exec", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("exec request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %s", res.Status)
	}
	var out struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.ExitCode != 0 || out.Stdout != "hello\n" || out.Stderr != "warn\n" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func fakeExecAgent(t *testing.T, base, enroll string) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + enroll}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(base, "/agent/ws"), h)
	if err != nil {
		t.Errorf("dial: %v", err)
		return
	}
	defer ws.Close()
	_ = ws.WriteJSON(protocol.Register{
		Type: protocol.TypeRegister, AgentID: "exec-host", Hostname: "exec-host",
		OS: "linux", CanExec: true,
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
		if env.Type == protocol.TypeExec {
			var req protocol.ExecRequest
			json.Unmarshal(data, &req)
			_ = ws.WriteJSON(protocol.ExecOutput{Type: protocol.TypeExecOut, ExecID: req.ExecID, Stream: "stdout", Data: "hello\n"})
			_ = ws.WriteJSON(protocol.ExecOutput{Type: protocol.TypeExecOut, ExecID: req.ExecID, Stream: "stderr", Data: "warn\n"})
			_ = ws.WriteJSON(protocol.ExecDone{Type: protocol.TypeExecDone, ExecID: req.ExecID, ExitCode: 0})
		}
	}
}
