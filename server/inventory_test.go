package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestInventoryRoundTrip(t *testing.T) {
	srv := New(Config{AdminToken: "admin", EnrollToken: "enroll", SecretPhrase: "s"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	go fakeInvAgent(t, ts.URL, "enroll")

	if !waitFor(3*time.Second, func() bool {
		hs := fetchHosts(t, ts.URL, "admin")
		return len(hs) == 1 && hs[0].Online
	}) {
		t.Fatal("agent did not register")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inventory?agent=inv-host", nil)
	req.Header.Set("Authorization", "Bearer admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("inventory request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %s", res.Status)
	}
	var out struct {
		Source   string             `json:"source"`
		Count    int                `json:"count"`
		Packages []protocol.Package `json:"packages"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Source != "dpkg" || out.Count != 2 || out.Packages[0].Name != "bash" {
		t.Fatalf("unexpected inventory: %+v", out)
	}
}

func fakeInvAgent(t *testing.T, base, enroll string) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + enroll}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(base, "/agent/ws"), h)
	if err != nil {
		t.Errorf("dial: %v", err)
		return
	}
	defer ws.Close()
	_ = ws.WriteJSON(protocol.Register{Type: protocol.TypeRegister, AgentID: "inv-host", Hostname: "inv-host", OS: "linux"})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.Type == protocol.TypeInventory {
			var req protocol.InventoryRequest
			json.Unmarshal(data, &req)
			_ = ws.WriteJSON(protocol.InventoryResponse{
				Type: protocol.TypeInventoryResp, ReqID: req.ReqID, Source: "dpkg",
				Packages: []protocol.Package{{Name: "bash", Version: "5.2"}, {Name: "curl", Version: "8.5"}},
			})
		}
	}
}
