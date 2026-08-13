package server

import (
	"context"
	"encoding/json"
	"fmt"
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

// A controller answers for every device it manages in one document, so the
// paths a preset writes have to pick one out by MAC and then reach into a
// nested table. This is the real shape of that answer, trimmed.
func TestControllerPathsSelectOneDeviceOfMany(t *testing.T) {
	const doc = `{"meta":{"rc":"ok"},"data":[
	  {"mac":"ac:8b:a9:58:f7:b2","model":"USL16LP","state":0,
	   "num_sta":0},
	  {"mac":"d8:b3:70:83:96:77","model":"US24P250","state":1,
	   "total_used_power":7.85,"general_temperature":45,
	   "system-stats":{"cpu":"59.6","mem":"47.7"},
	   "port_table":[{"port_idx":2,"name":"Uplink","poe_power":"7.85"}]},
	  {"mac":"58:d6:1f:1a:5b:99","model":"USPPDUP","state":1,
	   "outlet_ac_power_consumption":"225.843",
	   "system-stats":{"cpu":"1.6","mem":"85.7"},
	   "outlet_table":[
	     {"index":5,"name":"Rack","outlet_power":"33.873"},
	     {"index":8,"name":"Spare","outlet_power":"0.000"}]}]}`

	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want string
	}{
		// The switch's PoE draw, which is the reading its own SNMP agent does
		// not offer: it does not implement the PoE MIB.
		{"data[mac=d8:b3:70:83:96:77].total_used_power", "7.85"},
		{"data[mac=d8:b3:70:83:96:77].general_temperature", "45"},
		// A hyphen is an ordinary character in a key; only dots divide segments.
		{"data[mac=d8:b3:70:83:96:77].system-stats.cpu", "59.6"},
		{"data[mac=58:d6:1f:1a:5b:99].outlet_ac_power_consumption", "225.843"},
		// Two selectors in one path: the device, then the outlet on it.
		{"data[mac=58:d6:1f:1a:5b:99].outlet_table[name=Rack].outlet_power", "33.873"},
		{"data[mac=d8:b3:70:83:96:77].port_table[port_idx=2].poe_power", "7.85"},
	} {
		got, ok := jsonPath(v, c.path)
		if !ok {
			t.Errorf("%s: no value", c.path)
			continue
		}
		if s := fmt.Sprintf("%v", got); s != c.want {
			t.Errorf("%s = %s, want %s", c.path, s, c.want)
		}
	}

	// A MAC that is not there must fail rather than fall through to whichever
	// device happens to be first, which would report another device's power as
	// this one's.
	if _, ok := jsonPath(v, "data[mac=00:00:00:00:00:00].total_used_power"); ok {
		t.Error("an absent MAC matched something")
	}

	// The readings a preset asks for on a device the controller has adopted but
	// cannot currently reach are simply absent, and must be reported as such.
	if _, ok := jsonPath(v, "data[mac=ac:8b:a9:58:f7:b2].total_used_power"); ok {
		t.Error("a disconnected device reported PoE draw")
	}
}

// A percentage becomes a bar and a temperature stays a figure; that difference
// is carried entirely by the maximum, so a probe that declares one has to reach
// the reading.
func TestMaximumReachesTheReading(t *testing.T) {
	r := readingFrom(JSONProbe{Label: "CPU", Path: "x", Unit: "%", Max: 100}, "59.6")
	if !r.Numeric || r.Max != 100 || r.Num != 59.6 {
		t.Fatalf("got %+v", r)
	}
	if r.Text != "59.6%" {
		t.Errorf("text = %q", r.Text)
	}
	if plain := readingFrom(JSONProbe{Label: "Temp", Path: "x"}, 45.0); plain.Max != 0 {
		t.Errorf("a probe with no maximum got one: %+v", plain)
	}
}

// The whole chain a preset sets up: sign in, keep the session, fetch the
// controller's answer for every device, and come back with this device's
// readings — the percentages as bars and the watts as a figure.
func TestControllerReadingsEndToEnd(t *testing.T) {
	const mac = "58:d6:1f:1a:5b:99"
	var logins int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("signed in with %s, want POST", r.Method)
		}
		logins++
		http.SetCookie(w, &http.Cookie{Name: "unifises", Value: "s"})
		w.Write([]byte(`{"meta":{"rc":"ok"}}`))
	})
	mux.HandleFunc("/api/s/default/stat/device", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("unifises"); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[{"mac":"aa:bb:cc:dd:ee:ff","outlet_ac_power_consumption":"1.0"},
			{"mac":"` + mac + `","outlet_ac_power_consumption":"225.843",
			 "system-stats":{"cpu":"1.6","mem":"85.7"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	probes := []JSONProbe{
		{Label: "Load", Path: "data[mac=" + mac + "].outlet_ac_power_consumption", Unit: "W"},
		{Label: "CPU", Path: "data[mac=" + mac + "].system-stats.cpu", Unit: "%", Max: 100},
		{Label: "Memory", Path: "data[mac=" + mac + "].system-stats.mem", Unit: "%", Max: 100},
	}
	auth := JSONAuth{Mode: "login", LoginURL: srv.URL + "/api/login",
		LoginBody: `{"username":"ro","password":"x"}`}
	sess := newSessions()

	readings, msg := probeJSONAuth(context.Background(), "pdu",
		srv.URL+"/api/s/default/stat/device", probes, auth, sess)
	if msg != "" {
		t.Fatalf("message: %s", msg)
	}
	if len(readings) != 3 {
		t.Fatalf("got %d readings: %+v", len(readings), readings)
	}
	// The device's own load, not the first device in the list. Shown to two
	// decimals, which is as much precision as a wattage deserves.
	if readings[0].Text != "225.84W" || readings[0].Num != 225.843 {
		t.Errorf("load = %+v", readings[0])
	}
	// A declared full scale is what makes this a bar rather than a figure.
	if readings[1].Max != 100 || !readings[1].Numeric {
		t.Errorf("cpu = %+v", readings[1])
	}
	if readings[2].Text != "85.7%" {
		t.Errorf("memory = %+v", readings[2])
	}
	// A second poll must reuse the session rather than sign in again: a
	// controller asked for a new one every minute eventually stops issuing them.
	if _, msg := probeJSONAuth(context.Background(), "pdu",
		srv.URL+"/api/s/default/stat/device", probes, auth, sess); msg != "" {
		t.Fatalf("second poll: %s", msg)
	}
	if logins != 1 {
		t.Errorf("signed in %d times, want 1", logins)
	}
}

// One probe reading a whole table. A PDU has twenty outlets and writing twenty
// lines to read them is both tedious and a standing invitation to get one
// subtly wrong.
func TestWildcardReadsAWholeTable(t *testing.T) {
	const doc = `{"data":[{"mac":"aa:bb","outlet_table":[
	  {"index":1,"name":"USB 1"},
	  {"index":5,"name":"Rack","outlet_power":"33.873"},
	  {"index":7,"name":"Tank","outlet_power":"55.414"},
	  {"index":8,"name":"Spare","outlet_power":"0.000"}]}]}`
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	p := JSONProbe{Label: "Outlets", Unit: "W",
		Path: "data[mac=aa:bb].outlet_table[*name].outlet_power"}
	got, wild := expandProbe(v, p)
	if !wild {
		t.Fatal("the wildcard was not recognised")
	}
	// The USB outlet reports no wattage at all and is left out rather than
	// shown blank; the idle one reports zero and is a fact worth keeping.
	if len(got) != 3 {
		t.Fatalf("got %d readings: %+v", len(got), got)
	}
	for i, want := range []struct{ label, text string }{
		{"Rack", "33.87W"}, {"Tank", "55.41W"}, {"Spare", "0W"},
	} {
		if got[i].Label != want.label || got[i].Text != want.text {
			t.Errorf("reading %d = %+v, want %s %s", i, got[i], want.label, want.text)
		}
		// Every reading carries the group, which is what lets the card draw
		// them together instead of as twenty unrelated rows.
		if got[i].Group != "Outlets" {
			t.Errorf("reading %d has group %q", i, got[i].Group)
		}
		if !got[i].Numeric {
			t.Errorf("reading %d was not read as a number", i)
		}
	}

	// A path with no wildcard must be left to the ordinary lookup.
	if _, wild := expandProbe(v, JSONProbe{Path: "data[mac=aa:bb].outlet_table[name=Rack].outlet_power"}); wild {
		t.Error("an ordinary selector was treated as a wildcard")
	}
	// A wildcard over something that is not a table — here a string — is a miss,
	// not a panic.
	if rs, wild := expandProbe(v, JSONProbe{Path: "data[mac=aa:bb].mac[*name].x"}); !wild || len(rs) != 0 {
		t.Errorf("wildcard over a non-table = %v %v", rs, wild)
	}
	// And one over a path that does not resolve at all.
	if rs, wild := expandProbe(v, JSONProbe{Path: "nowhere[*name].x"}); !wild || len(rs) != 0 {
		t.Errorf("wildcard over an absent path = %v %v", rs, wild)
	}
	// And a table with no such value anywhere reports nothing rather than rows
	// of blanks, so the poll can say the path stopped matching.
	if rs, _ := expandProbe(v, JSONProbe{Path: "data[mac=aa:bb].outlet_table[*name].nope"}); len(rs) != 0 {
		t.Errorf("a path matching nothing produced %d readings", len(rs))
	}
}

// Rows with no usable label fall back to their position, so a table without a
// name field still reads rather than collapsing into blanks.
func TestWildcardFallsBackToPosition(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"cells":[{"v":1.5},{"v":2.5}]}`), &v)
	got, _ := expandProbe(v, JSONProbe{Label: "Cells", Path: "cells[*name].v"})
	if len(got) != 2 || got[0].Label != "1" || got[1].Label != "2" {
		t.Fatalf("got %+v", got)
	}
}
