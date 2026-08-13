package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// removeTestServer wires the stores a removal touches, so the test exercises
// the tidying rather than a handler with everything stubbed out.
func removeTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		store:    NewStore(60, time.Minute, nil),
		cfg:      Config{AdminToken: testAdminToken},
		svc:      newSvcWatcher(),
		alerter:  NewAlerter(AlertConfig{}),
		prefs:    newHostPrefs(t.TempDir()),
		auditLog: newAuditLog(nil),
	}
}

func removeRequest(agent string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/api/hosts?agent="+agent, nil)
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	return r
}

// A decommissioned machine is offline, and removing it must take everything
// keyed by its agent id with it.
func TestRemoveOfflineHostAndItsState(t *testing.T) {
	s := removeTestServer(t)
	s.store.register(protocol.Register{AgentID: "old", Hostname: "retired"}, nil)
	s.svc.set("old", map[string]bool{"nginx": false})
	s.prefs.set("old", HostPref{CPU: 55})
	s.alerter.states[alertKey{agent: "old", rule: "cpu"}] = &alertState{firing: true}

	w := httptest.NewRecorder()
	s.handleHosts(w, removeRequest("old"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(s.store.views()) != 0 {
		t.Errorf("host still listed: %+v", s.store.views())
	}
	if len(s.svc.states("old")) != 0 {
		t.Error("watched services survived the removal")
	}
	// Thresholds left behind would be silently reapplied to whatever next took
	// this agent id.
	if p := s.prefs.get("old"); p.CPU != 0 {
		t.Errorf("thresholds survived the removal: %+v", p)
	}
	// A removed host that goes on firing — or resolves later and sends a
	// recovery notice for a machine that no longer exists — is worse than one
	// that was never removed.
	if len(s.alerter.states) != 0 {
		t.Errorf("alert state survived the removal: %+v", s.alerter.states)
	}
}

// Removing a host whose agent is still connected is refused rather than done,
// because the agent re-registers within seconds and the card comes back — which
// reads as the button being broken.
func TestRemoveRefusesAConnectedHost(t *testing.T) {
	s := removeTestServer(t)
	s.store.register(protocol.Register{AgentID: "live", Hostname: "busy"}, &agentConn{})

	w := httptest.NewRecorder()
	s.handleHosts(w, removeRequest("live"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	if len(s.store.views()) != 1 {
		t.Error("a connected host was removed anyway")
	}
	// The refusal has to say what to do about it, or it is just a locked door.
	if b := w.Body.String(); !strings.Contains(b, "stop its agent") {
		t.Errorf("refusal does not say how to proceed: %q", b)
	}
}

func TestRemoveUnknownHostIsNotFound(t *testing.T) {
	s := removeTestServer(t)
	w := httptest.NewRecorder()
	s.handleHosts(w, removeRequest("ghost"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestRemoveNeedsAnAgentAndAToken(t *testing.T) {
	s := removeTestServer(t)
	w := httptest.NewRecorder()
	s.handleHosts(w, removeRequest(""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("no agent: status %d, want 400", w.Code)
	}
	// Deleting a host is not something an unauthenticated request may do.
	w2 := httptest.NewRecorder()
	s.handleHosts(w2, httptest.NewRequest(http.MethodDelete, "/api/hosts?agent=old", nil))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", w2.Code)
	}
}

// A GET must still list hosts: the method branch has to be a branch, not a
// takeover of the endpoint.
func TestListingStillWorks(t *testing.T) {
	s := removeTestServer(t)
	s.store.register(protocol.Register{AgentID: "a", Hostname: "one"}, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/hosts", nil)
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	s.handleHosts(w, r)
	var views []protocol.HostView
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Hostname != "one" {
		t.Fatalf("views = %+v", views)
	}
}

// Samples are the bulky part of a removal and the part that would otherwise
// accumulate forever on a hub that has outlived a few machines.
func TestRemoveDeletesHistoryForThatHostOnly(t *testing.T) {
	h, err := OpenHistory(t.TempDir()+"/h.db", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.Insert("old", &protocol.Metrics{CPUPercent: 10})
	h.Insert("keep", &protocol.Metrics{CPUPercent: 20})

	if err := h.DeleteAgent("old"); err != nil {
		t.Fatal(err)
	}
	gone, err := h.Query("old", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("removed host kept %d samples", len(gone))
	}
	// The one thing worse than not deleting is deleting somebody else's.
	kept, err := h.Query("keep", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("another host lost history: %d samples", len(kept))
	}
	// A hub with no database at all must not panic on removal.
	var nilHist *History
	if err := nilHist.DeleteAgent("old"); err != nil {
		t.Errorf("nil history: %v", err)
	}
}
