package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func netCheckServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		cfg:       Config{AdminToken: testAdminToken},
		netChecks: newNetChecks(dir),
		auditLog:  newAuditLog(nil),
	}, dir
}

func saveCheck(t *testing.T, s *Server, body string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/netchecks", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	w := httptest.NewRecorder()
	s.handleNetChecks(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("save: status %d: %s", w.Code, w.Body.String())
	}
}

// Turning SNMP off has to actually turn it off.
//
// It did not: the form cleared the community box, and an empty box is how an
// edit says "I did not retype this", so the untouched-secret rule put the
// stored community straight back and the device went on being polled — with the
// version reverting to the automatic fallback, which is what showed up as SNMP
// switching itself back to v2c.
func TestTurningSNMPOffClearsTheCredentials(t *testing.T) {
	s, dir := netCheckServer(t)
	saveCheck(t, s, `{"name":"switch","address":"10.0.0.1","snmp":"public","snmp_version":"2c"}`)

	all := s.netChecks.list()
	if len(all) != 1 {
		t.Fatalf("expected one check, got %d", len(all))
	}
	id := all[0].NetCheck.ID
	if c, _ := s.netChecks.byID(id); !snmpConfigured(c) {
		t.Fatal("SNMP was not configured to begin with")
	}

	// What the form sends for "SNMP off": the boxes cleared, and the flag that
	// says this was a decision rather than an omission.
	saveCheck(t, s, `{"id":"`+id+`","name":"switch","address":"10.0.0.1","snmp":"","snmp_version":"","snmp_off":true}`)

	c, ok := s.netChecks.byID(id)
	if !ok {
		t.Fatal("check disappeared")
	}
	if c.SNMP != "" || c.SNMPUser != "" || c.SNMPAuthPass != "" || c.SNMPPrivPass != "" {
		t.Errorf("credentials survived: %+v", c)
	}
	if snmpConfigured(c) {
		t.Error("still configured for SNMP after being turned off")
	}
	// And it has to survive the round trip through the file, or it comes back
	// on the next restart.
	reloaded := newNetChecks(dir)
	if c2, ok := reloaded.byID(id); !ok || snmpConfigured(c2) {
		t.Errorf("SNMP came back after reload: %+v", c2)
	}
}

// The guard the clearing must not break: an ordinary edit that did not retype
// the community keeps it, which is what makes redacting secrets on the way out
// safe in the first place.
func TestAnEditThatDidNotRetypeKeepsTheCommunity(t *testing.T) {
	s, _ := netCheckServer(t)
	saveCheck(t, s, `{"name":"switch","address":"10.0.0.1","snmp":"public","snmp_version":"2c"}`)
	id := s.netChecks.list()[0].NetCheck.ID

	// Renaming it, with the secret boxes left as the dashboard rendered them.
	saveCheck(t, s, `{"id":"`+id+`","name":"core switch","address":"10.0.0.1","snmp":"`+secretPlaceholder+`","snmp_version":"2c"}`)
	c, _ := s.netChecks.byID(id)
	if c.SNMP != "public" {
		t.Errorf("community = %q, want it kept", c.SNMP)
	}
	if c.Name != "core switch" {
		t.Errorf("name = %q", c.Name)
	}

	// An empty box is the same statement, and must also keep it.
	saveCheck(t, s, `{"id":"`+id+`","name":"core switch","address":"10.0.0.1","snmp":"","snmp_version":"2c"}`)
	if c, _ := s.netChecks.byID(id); c.SNMP != "public" {
		t.Errorf("community after an empty box = %q, want it kept", c.SNMP)
	}
}

// The flag describes one save; it must never become part of the device.
func TestTheOffFlagIsNotStored(t *testing.T) {
	s, _ := netCheckServer(t)
	saveCheck(t, s, `{"name":"pdu","address":"10.0.0.2","snmp_off":true}`)
	id := s.netChecks.list()[0].NetCheck.ID
	c, _ := s.netChecks.byID(id)
	if c.SNMPOff {
		t.Error("the request flag was stored on the device")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "snmp_off") {
		t.Errorf("the flag is serialised into storage: %s", b)
	}
}

// v3 turns off too, which is the case where the credentials are a passphrase
// rather than a community string.
func TestTurningOffClearsV3Credentials(t *testing.T) {
	s, _ := netCheckServer(t)
	saveCheck(t, s, `{"name":"ups","address":"10.0.0.3","snmp_version":"3","snmp_user":"monitor",
		"snmp_auth_proto":"SHA","snmp_auth_pass":"authsecret","snmp_priv_proto":"AES","snmp_priv_pass":"privsecret"}`)
	id := s.netChecks.list()[0].NetCheck.ID
	if c, _ := s.netChecks.byID(id); !snmpConfigured(c) {
		t.Fatal("v3 was not configured to begin with")
	}
	saveCheck(t, s, `{"id":"`+id+`","name":"ups","address":"10.0.0.3","snmp_off":true}`)
	c, _ := s.netChecks.byID(id)
	if c.SNMPUser != "" || c.SNMPAuthPass != "" || c.SNMPPrivPass != "" ||
		c.SNMPAuthProto != "" || c.SNMPPrivProto != "" || c.SNMPVersion != "" {
		t.Errorf("v3 credentials survived: %+v", c)
	}
	if snmpConfigured(c) {
		t.Error("still configured for SNMP after being turned off")
	}
}
