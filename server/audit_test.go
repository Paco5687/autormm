package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testAudit(t *testing.T) *auditLog {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newAuditLog(db)
}

func TestAuditRecordsAndReadsBack(t *testing.T) {
	a := testAudit(t)
	a.record(AuditEvent{
		TS: time.Now().Unix(), Actor: "wren", Action: "reboot",
		Target: "tron", Remote: "192.0.2.10", Outcome: "ok",
	})
	got, err := a.query(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Actor != "wren" || got[0].Target != "tron" || got[0].Remote != "192.0.2.10" {
		t.Errorf("event came back wrong: %+v", got[0])
	}
}

// Newest first, because the question is always "what just happened".
func TestAuditReturnsNewestFirst(t *testing.T) {
	a := testAudit(t)
	now := time.Now().Unix()
	for i, act := range []string{"oldest", "middle", "newest"} {
		a.record(AuditEvent{TS: now + int64(i), Actor: "x", Action: act})
	}
	got, _ := a.query(10, "")
	if len(got) != 3 || got[0].Action != "newest" || got[2].Action != "oldest" {
		t.Errorf("wrong order: %v", []string{got[0].Action, got[1].Action, got[2].Action})
	}
}

// An audit write must never be the reason a reboot does not happen, so a hub
// with no database still runs — it just keeps no history.
func TestAuditWithoutDatabaseIsHarmless(t *testing.T) {
	a := newAuditLog(nil)
	a.record(AuditEvent{TS: time.Now().Unix(), Actor: "x", Action: "reboot"})
	got, err := a.query(10, "")
	if err != nil || len(got) != 0 {
		t.Errorf("query = %v, %v; want empty and no error", got, err)
	}
	// And a nil log at all, which is what a zero-value Server holds.
	var nilLog *auditLog
	nilLog.record(AuditEvent{Action: "reboot"})
}

// A failed login is exactly the event worth keeping, so denied outcomes must be
// recorded rather than filtered out as uninteresting.
func TestAuditKeepsDeniedOutcomes(t *testing.T) {
	a := testAudit(t)
	a.record(AuditEvent{TS: time.Now().Unix(), Actor: "?", Action: "login", Outcome: "denied"})
	got, _ := a.query(10, "login")
	if len(got) != 1 || got[0].Outcome != "denied" {
		t.Fatalf("denied login not recorded: %+v", got)
	}
}

func TestAuditFiltersByAction(t *testing.T) {
	a := testAudit(t)
	now := time.Now().Unix()
	a.record(AuditEvent{TS: now, Actor: "x", Action: "reboot"})
	a.record(AuditEvent{TS: now, Actor: "x", Action: "login"})
	got, _ := a.query(10, "reboot")
	if len(got) != 1 || got[0].Action != "reboot" {
		t.Errorf("filter returned %+v", got)
	}
}

// The standing admin token is not a person. Attributing its actions to a
// username would put a name in the record that nobody actually typed.
func TestActorNamesTheAdminTokenHonestly(t *testing.T) {
	s := &Server{cfg: Config{AdminToken: "tok"}}
	r := httptest.NewRequest(http.MethodPost, "/api/reboot", nil)
	r.Header.Set("Authorization", "Bearer tok")
	if got := actorOf(r, s); got != "admin token" {
		t.Errorf("actor = %q, want %q", got, "admin token")
	}
}

// Behind a reverse proxy — the normal deployment — RemoteAddr is the proxy, so
// every event would otherwise be attributed to the same address.
func TestClientIPPrefersForwardedAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/reboot", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if got := clientIP(r); got != "127.0.0.1" {
		t.Errorf("clientIP = %q, want the port stripped", got)
	}
	r.Header.Set("X-Forwarded-For", "192.0.2.44, 198.51.100.1")
	if got := clientIP(r); got != "192.0.2.44" {
		t.Errorf("clientIP = %q, want the original client", got)
	}
}

func TestAuditEndpointRequiresAuth(t *testing.T) {
	s := testServer()
	w := httptest.NewRecorder()
	s.handleAudit(w, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
