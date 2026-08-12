package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
