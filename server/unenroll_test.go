package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// A connection a test can both receive on and answer through, standing in for
// an agent that is listening.
func testConn(execReg *execRegistry) *agentConn {
	return &agentConn{
		send:    make(chan []byte, 8),
		done:    make(chan struct{}),
		execReg: execReg,
	}
}

func unenrollRequest(agent string, authorised bool) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/hosts/unenroll?agent="+agent, nil)
	if authorised {
		r.Header.Set("Authorization", "Bearer "+testAdminToken)
	}
	return r
}

// answerUninstall plays the agent: it waits for the uninstall request on the
// connection, replies with the given exit code, and optionally drops off the
// hub the way a real agent does once its teardown has stopped the service.
func answerUninstall(t *testing.T, s *Server, agentID string, c *agentConn, code int, errStr string, disconnect bool) {
	t.Helper()
	go func() {
		select {
		case b := <-c.send:
			var req protocol.UninstallRequest
			if json.Unmarshal(b, &req) != nil || req.Type != protocol.TypeUninstall {
				return
			}
			if col := s.execReg.get(req.ExecID); col != nil {
				col.finish(code, errStr)
			}
			if disconnect {
				s.store.disconnect(agentID, c)
			}
		case <-time.After(5 * time.Second):
		}
	}()
}

// Unenrolling uninstalls software on someone's machine. It must not be
// reachable without the admin credential — the handlers on this hub each check
// for themselves, so a new one that forgets is a hole with no net under it.
func TestUnenrollRequiresAuth(t *testing.T) {
	s := removeTestServer(t)
	s.store.register(protocol.Register{AgentID: "box"}, testConn(s.execReg))

	w := httptest.NewRecorder()
	s.unenrollHost(w, unenrollRequest("box", false))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if _, ok := s.store.hosts["box"]; !ok {
		t.Error("the host was removed by an unauthenticated request")
	}
}

// An offline host has nobody to carry out the uninstall. Accepting the request
// would drop it from the dashboard and leave the agent installed and running —
// the exact outcome unenrolling exists to avoid — so it is refused and the
// operator is pointed at Remove instead.
func TestUnenrollOfflineHostIsRefused(t *testing.T) {
	s := removeTestServer(t)
	c := testConn(s.execReg)
	s.store.register(protocol.Register{AgentID: "gone"}, c)
	s.store.disconnect("gone", c)

	w := httptest.NewRecorder()
	s.unenrollHost(w, unenrollRequest("gone", true))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if _, ok := s.store.hosts["gone"]; !ok {
		t.Error("an offline host was removed by a request that was supposed to be refused")
	}
}

// The whole point, end to end: the agent accepts, goes away, and the host and
// everything keyed to it go with it.
func TestUnenrollRemovesHostOnceTheAgentIsGone(t *testing.T) {
	s := removeTestServer(t)
	c := testConn(s.execReg)
	s.store.register(protocol.Register{AgentID: "wrongbox", Hostname: "wrongbox"}, c)
	answerUninstall(t, s, "wrongbox", c, 0, "", true)

	w := httptest.NewRecorder()
	s.unenrollHost(w, unenrollRequest("wrongbox", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if _, ok := s.store.hosts["wrongbox"]; ok {
		t.Error("the host is still on the dashboard after a successful unenroll")
	}
}

// An agent that cannot uninstall itself — installed by hand, or on a platform
// with no installer to reverse — must leave the host exactly where it was.
// Forgetting it anyway would hide a machine that is still reporting in.
func TestUnenrollKeepsHostWhenTheAgentRefuses(t *testing.T) {
	s := removeTestServer(t)
	c := testConn(s.execReg)
	s.store.register(protocol.Register{AgentID: "handmade"}, c)
	answerUninstall(t, s, "handmade", c, 1, "no autormm-agent service found", false)

	w := httptest.NewRecorder()
	s.unenrollHost(w, unenrollRequest("handmade", true))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", w.Code, w.Body.String())
	}
	if _, ok := s.store.hosts["handmade"]; !ok {
		t.Error("the host was removed even though its agent refused to uninstall")
	}
}

// A Windows host is two installs — the tray app in the user's session and the
// elevated helper as a SYSTEM service — and each can only remove its own half.
// Both have to be asked, or the one that was not stays on the machine.
func TestUnenrollAsksBothWindowsConnections(t *testing.T) {
	s := removeTestServer(t)
	tray, elev := testConn(s.execReg), testConn(s.execReg)
	s.store.register(protocol.Register{AgentID: "win11", CanStream: true}, tray)
	s.store.register(protocol.Register{AgentID: "win11", Elevated: true, CanExec: true}, elev)

	answerUninstall(t, s, "win11", tray, 0, "", true)
	answerUninstall(t, s, "win11", elev, 0, "", true)

	w := httptest.NewRecorder()
	s.unenrollHost(w, unenrollRequest("win11", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if _, ok := s.store.hosts["win11"]; ok {
		t.Error("the host survived an unenroll both of its connections accepted")
	}
}

// The console worker runs from the elevated helper's own binary and dies with
// it. Asking it to uninstall would have it delete its parent's install from
// underneath the parent, so it is not among the connections that get asked.
func TestUnenrollSkipsTheConsoleWorker(t *testing.T) {
	s := removeTestServer(t)
	tray, cons := testConn(s.execReg), testConn(s.execReg)
	s.store.register(protocol.Register{AgentID: "win11"}, tray)
	s.store.register(protocol.Register{AgentID: "win11", Console: true}, cons)

	conns := s.store.controlConns("win11")
	for _, c := range conns {
		if c == cons {
			t.Fatal("the console worker was asked to uninstall itself")
		}
	}
	if len(conns) != 1 {
		t.Errorf("asked %d connections, want 1 (the interactive agent)", len(conns))
	}
}
