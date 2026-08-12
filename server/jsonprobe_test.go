package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Shaped like an Apex status document: sensors as a list of objects, values
// quoted, which is how a great deal of small hardware reports itself.
const apexJSON = `{"istat":{"hostname":"apex","inputs":[
  {"did":"base_Temp","type":"Temp","name":"Tmp","value":"78.2"},
  {"did":"base_pH","type":"pH","name":"pH","value":8.21},
  {"did":"base_ORP","type":"ORP","name":"ORP","value":382}],
 "outputs":[{"name":"Heater","status":["ON","",""]}]}}`

func decodeApex(t *testing.T) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(apexJSON), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Selecting by a field, not a position. A device reporting its sensors as a
// list is free to reorder them, and a path written by index would then quietly
// start reading a different sensor — a tank showing pH where it says temp.
func TestPathSelectsByFieldNotPosition(t *testing.T) {
	doc := decodeApex(t)
	for path, want := range map[string]string{
		"istat.inputs[name=Tmp].value": "78.2",
		"istat.inputs[name=pH].value":  "8.21",
		"istat.inputs[name=ORP].value": "382",
		"istat.hostname":               "apex",
	} {
		v, ok := jsonPath(doc, path)
		if !ok {
			t.Errorf("%s did not resolve", path)
			continue
		}
		if got := readingFrom(JSONProbe{Path: path}, v); got.Text != want {
			t.Errorf("%s = %q, want %q", path, got.Text, want)
		}
	}
}

func TestPathSupportsIndexes(t *testing.T) {
	doc := decodeApex(t)
	if v, ok := jsonPath(doc, "istat.inputs[0].name"); !ok || v != "Tmp" {
		t.Errorf("bracket index = %v %v", v, ok)
	}
	if v, ok := jsonPath(doc, "istat.outputs[name=Heater].status[0]"); !ok || v != "ON" {
		t.Errorf("nested index = %v %v", v, ok)
	}
}

// A path that no longer matches must say so rather than silently reading
// nothing, or a probe that broke looks the same as a device that is fine.
func TestMissingPathIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(apexJSON))
	}))
	defer srv.Close()

	got, msg := probeJSON(context.Background(), srv.URL, []JSONProbe{
		{Label: "Tank temp", Path: "istat.inputs[name=Tmp].value", Unit: "°F"},
		{Label: "Salinity", Path: "istat.inputs[name=Salinity].value"},
	})
	if len(got) != 1 || got[0].Label != "Tank temp" {
		t.Fatalf("readings = %+v", got)
	}
	if got[0].Text != "78.2°F" || !got[0].Numeric {
		t.Errorf("quoted number was not read as one: %+v", got[0])
	}
	if msg == "" {
		t.Error("a path that matched nothing was not reported")
	}
}

// Anything that is not JSON, or an error page, is said plainly.
func TestNonJSONIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>login</html>"))
	}))
	defer srv.Close()
	if _, msg := probeJSON(context.Background(), srv.URL, []JSONProbe{{Label: "x", Path: "a"}}); msg == "" {
		t.Error("an HTML page was accepted as JSON")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	if _, msg := probeJSON(context.Background(), bad.URL, []JSONProbe{{Label: "x", Path: "a"}}); msg == "" {
		t.Error("a 401 was not reported")
	}
}

// Numbers are printed the way a person would write them.
func TestNumbersAreTrimmed(t *testing.T) {
	for in, want := range map[float64]string{78.2: "78.2", 8.21: "8.21", 382: "382", 0: "0", 8.0: "8"} {
		if got := trimFloat(in); got != want {
			t.Errorf("trimFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

// Booleans and status words are readings too, even though they cannot be bars.
func TestNonNumericValuesStillRead(t *testing.T) {
	r := readingFrom(JSONProbe{Label: "Heater"}, "ON")
	if r.Text != "ON" || r.Numeric {
		t.Errorf("got %+v", r)
	}
	if b := readingFrom(JSONProbe{Label: "Leak"}, true); b.Text != "yes" {
		t.Errorf("got %+v", b)
	}
}

// A status URL with no values to read, or values with no URL, does nothing —
// and doing nothing quietly looks exactly like a device that is not answering.
func TestHalfConfiguredProbeSaysSo(t *testing.T) {
	// The pairing is checked before any request is made, so an unreachable
	// address is irrelevant to the outcome.
	readings, msg := probeJSON(context.Background(), "", []JSONProbe{{Label: "Temp", Path: "a"}})
	if len(readings) != 0 || msg != "" {
		t.Errorf("probeJSON with no URL should be inert: %v %q", readings, msg)
	}
	readings2, msg2 := probeJSON(context.Background(), "http://192.0.2.1/x", nil)
	if len(readings2) != 0 || msg2 != "" {
		t.Errorf("probeJSON with no probes should be inert: %v %q", readings2, msg2)
	}
}

// Dotted numeric indices, which is the form the placeholder and the docs both
// advertise. The bracket form was covered and worked; this one was not, and
// did not — a path like heads.0.extruders resolved a key named "0" against an
// array and failed.
func TestDottedIndexWalksArrays(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"heads":[{"extruders":[
		{"hotend":{"temperature":{"current":205.0,"target":210.0}}},
		{"hotend":{"temperature":{"current":0.0,"target":0.0}}}]}],
		"bed":{"temperature":{"current":55.5,"target":60.0}}}`), &doc); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]float64{
		"heads.0.extruders.0.hotend.temperature.current": 205,
		"heads.0.extruders.1.hotend.temperature.current": 0,
		"bed.temperature.current":                        55.5,
		"bed.temperature.target":                         60,
	} {
		v, ok := jsonPath(doc, path)
		if !ok {
			t.Errorf("%s did not resolve", path)
			continue
		}
		if f, isNum := v.(float64); !isNum || f != want {
			t.Errorf("%s = %v, want %v", path, v, want)
		}
	}
	// An index past the end is a miss, not a panic.
	if _, ok := jsonPath(doc, "heads.5.extruders"); ok {
		t.Error("an out-of-range index resolved")
	}
	// And the bracket form still works alongside it.
	if v, ok := jsonPath(doc, "heads[0].extruders[0].hotend.temperature.target"); !ok || v != 210.0 {
		t.Errorf("bracket form = %v %v", v, ok)
	}
}

// A controller that hands out a session must not be asked for a new one on
// every poll; it will eventually start refusing.
func TestSessionIsReusedAcrossPolls(t *testing.T) {
	var logins int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		logins++
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value != "abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"outlets":[{"index":1,"power":42.5}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sess := newSessions()
	auth := JSONAuth{Mode: "login", LoginURL: srv.URL + "/api/login", LoginBody: `{"username":"u"}`}
	probes := []JSONProbe{{Label: "Outlet 1", Path: "outlets[index=1].power", Unit: "W"}}

	for i := 0; i < 3; i++ {
		got, msg := probeJSONAuth(context.Background(), "pdu", srv.URL+"/api/status", probes, auth, sess)
		if msg != "" {
			t.Fatalf("poll %d: %s", i, msg)
		}
		if len(got) != 1 || got[0].Text != "42.5W" {
			t.Fatalf("poll %d: %+v", i, got)
		}
	}
	if logins != 1 {
		t.Errorf("logged in %d times over three polls, want 1", logins)
	}
}

// An expired session is ordinary, not a failure: drop it and log in again
// rather than reporting the device as broken.
func TestExpiredSessionLogsInAgain(t *testing.T) {
	var logins int
	valid := "first"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		logins++
		http.SetCookie(w, &http.Cookie{Name: "session", Value: valid, Path: "/"})
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || c.Value != valid {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"temp":21}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sess := newSessions()
	auth := JSONAuth{Mode: "login", LoginURL: srv.URL + "/api/login", LoginBody: `{}`}
	probes := []JSONProbe{{Label: "Temp", Path: "temp"}}

	if _, msg := probeJSONAuth(context.Background(), "x", srv.URL+"/api/status", probes, auth, sess); msg != "" {
		t.Fatalf("first poll: %s", msg)
	}
	valid = "rotated" // the controller invalidated what we hold
	got, msg := probeJSONAuth(context.Background(), "x", srv.URL+"/api/status", probes, auth, sess)
	if msg != "" {
		t.Fatalf("after expiry: %s", msg)
	}
	if len(got) != 1 {
		t.Fatalf("no reading after re-login: %+v", got)
	}
	if logins != 2 {
		t.Errorf("logged in %d times, want 2", logins)
	}
}

// Basic and bearer go on the request rather than through a session.
func TestRequestAuthModes(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()
	probes := []JSONProbe{{Label: "V", Path: "v"}}

	probeJSONAuth(context.Background(), "a", srv.URL, probes, JSONAuth{Mode: "bearer", Token: "tok"}, newSessions())
	if gotAuth != "Bearer tok" {
		t.Errorf("bearer header = %q", gotAuth)
	}
	probeJSONAuth(context.Background(), "b", srv.URL, probes, JSONAuth{Mode: "basic", User: "u", Pass: "p"}, newSessions())
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("basic header = %q", gotAuth)
	}
}
