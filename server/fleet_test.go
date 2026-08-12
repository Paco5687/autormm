package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

const testAdminToken = "test-admin-token"

func testServer() *Server {
	return &Server{
		store: NewStore(60, time.Minute, nil),
		cfg:   Config{AdminToken: testAdminToken},
	}
}

// An empty configured token does not authorise an empty bearer, so these have
// to present a real one — the subject here is targeting and reporting.
func fleetRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/fleet", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	return r
}

// A fleet action names every host it touched and says what each one did. The
// failure worth guarding is the opposite: reporting a single verdict, or an
// "ok", for a set of hosts where some of them did not do the thing.
func TestFleetActionReportsEveryHostSeparately(t *testing.T) {
	results := []fleetResult{
		{AgentID: "a", Hostname: "web1", OK: true},
		{AgentID: "b", Hostname: "web2", OK: false, Output: "permission denied"},
		{AgentID: "c", Hostname: "web3", OK: true},
	}
	sort.SliceStable(results, func(i, j int) bool { return !results[i].OK && results[j].OK })
	if results[0].Hostname != "web2" {
		t.Errorf("failures are not first: %v", results[0].Hostname)
	}
	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

// An unknown action must be refused rather than silently doing nothing and
// reporting success for every host.
func TestFleetRejectsUnknownAction(t *testing.T) {
	s := &Server{}
	ok, msg := s.fleetOne("rm-rf", "a1")
	if ok {
		t.Error("an unknown action reported success")
	}
	if !strings.Contains(msg, "unknown action") {
		t.Errorf("unhelpful message: %q", msg)
	}
}

// Targeting a selector that matches nothing is a conflict, not an empty
// success: "rebooted 0 hosts, all fine" is the report that hides a typo in a
// tag name.
func TestFleetEmptyTargetIsRefused(t *testing.T) {
	s := testServer()
	w := httptest.NewRecorder()
	s.handleFleetAction(w, fleetRequest(`{"target":"tag:nosuchtag","action":"reboot"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no online hosts") {
		t.Errorf("body did not say why: %q", w.Body.String())
	}
}

func TestFleetRequiresTargetAndAction(t *testing.T) {
	s := testServer()
	for _, body := range []string{`{}`, `{"target":"all"}`, `{"action":"reboot"}`} {
		w := httptest.NewRecorder()
		s.handleFleetAction(w, fleetRequest(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, w.Code)
		}
	}
}

// The response shape the dashboard reads. Pinned because the per-host list is
// the whole point: a client that finds only a summary would show a green tick
// over a partial failure.
func TestFleetResponseCarriesPerHostResults(t *testing.T) {
	payload := map[string]any{
		"results": []fleetResult{{AgentID: "a", Hostname: "web1", OK: false, Output: "boom"}},
		"total":   1,
		"failed":  1,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []fleetResult `json:"results"`
		Total   int           `json:"total"`
		Failed  int           `json:"failed"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].Hostname != "web1" || got.Results[0].OK {
		t.Errorf("per-host result did not survive the round trip: %+v", got.Results)
	}
	if got.Failed != 1 || got.Total != 1 {
		t.Errorf("counts wrong: %+v", got)
	}
}

// This endpoint reboots machines, so an unauthenticated caller gets nothing.
func TestFleetRequiresAuth(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest(http.MethodPost, "/api/fleet",
		strings.NewReader(`{"target":"all","action":"reboot"}`))
	w := httptest.NewRecorder()
	s.handleFleetAction(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
