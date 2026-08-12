package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Reading values out of a device's own JSON.
//
// Plenty of hardware has no SNMP at all but does serve its state as JSON over
// HTTP — aquarium controllers, solar inverters, 3D printers, weather stations,
// anything built in the last decade with a web UI. A check that can only say
// "it answered" throws away everything interesting about those.
//
// So: fetch a URL, pull named values out of the response, and show them on the
// card like any other reading. Read-only, and it never sends anything but a GET.

// JSONProbe is one value to pull out of a JSON response.
type JSONProbe struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Unit  string `json:"unit,omitempty"`
	// Max turns the reading into a bar rather than a figure. Zero leaves it a
	// figure, which is right for a temperature and wrong for a percentage.
	Max float64 `json:"max,omitempty"`
}

// Reading is one value a probe pulled out.
type Reading struct {
	Label string  `json:"label"`
	Text  string  `json:"text"`
	Num   float64 `json:"num,omitempty"`
	Unit  string  `json:"unit,omitempty"`
	Max   float64 `json:"max,omitempty"`
	// Numeric distinguishes a value that can be drawn as a bar from one that is
	// only ever text, such as a status word.
	Numeric bool `json:"numeric,omitempty"`
}

// probeJSON fetches a URL and extracts the configured values.
//
// Errors are returned as a message rather than as an absence: a probe that
// stopped matching because the device changed its JSON should say so, not
// quietly show nothing.
func probeJSON(ctx context.Context, url string, probes []JSONProbe) ([]Reading, string) {
	if url == "" || len(probes) == 0 {
		return nil, ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err.Error()
	}
	req.Header.Set("User-Agent", "autormm-healthcheck")
	resp, err := appClient.Do(req)
	if err != nil {
		return nil, shortJSONError(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Sprintf("HTTP %d from the device", resp.StatusCode)
	}

	// Bounded: this is a status document, and a device answering with a
	// gigabyte of something is not one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, "could not read the response"
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "the response is not JSON"
	}

	var out []Reading
	var missing []string
	for _, p := range probes {
		v, ok := jsonPath(doc, p.Path)
		if !ok {
			missing = append(missing, p.Label)
			continue
		}
		out = append(out, readingFrom(p, v))
	}
	if len(missing) > 0 {
		return out, "no value at the path for: " + strings.Join(missing, ", ")
	}
	return out, ""
}

// readingFrom renders one extracted value.
func readingFrom(p JSONProbe, v any) Reading {
	r := Reading{Label: p.Label, Unit: p.Unit, Max: p.Max}
	switch n := v.(type) {
	case float64:
		r.Num, r.Numeric = n, true
	case bool:
		r.Text = map[bool]string{true: "yes", false: "no"}[n]
		return r
	case string:
		// Devices routinely quote their numbers; a temperature of "78.2" is
		// still a temperature.
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			r.Num, r.Numeric = f, true
		} else {
			r.Text = n
			return r
		}
	default:
		r.Text = fmt.Sprintf("%v", v)
		return r
	}
	r.Text = trimFloat(r.Num) + p.Unit
	return r
}

// trimFloat prints a number the way someone would write it: no trailing zeros,
// and no more precision than a sensor deserves.
func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// jsonPath walks a dotted path into a decoded document.
//
// Segments are keys or array indices, and a segment may carry a selector —
// inputs[name=Tmp] — which picks the array element whose field matches. The
// selector matters more than it looks: a device that reports its sensors as a
// list is free to reorder them, and a path written by index would then quietly
// start reading a different sensor.
func jsonPath(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		key, sel, hasSel := cutSelector(seg)
		if key != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = m[key]
			if !ok {
				return nil, false
			}
		}
		if !hasSel {
			// A bare number is an array index.
			if i, err := strconv.Atoi(seg); err == nil && key == "" {
				arr, ok := cur.([]any)
				if !ok || i < 0 || i >= len(arr) {
					return nil, false
				}
				cur = arr[i]
			}
			continue
		}
		next, ok := selectFrom(cur, sel)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// cutSelector splits "inputs[name=Tmp]" into its key and its selector.
func cutSelector(seg string) (key, sel string, ok bool) {
	i := strings.IndexByte(seg, '[')
	if i < 0 || !strings.HasSuffix(seg, "]") {
		return seg, "", false
	}
	return seg[:i], seg[i+1 : len(seg)-1], true
}

// selectFrom picks an array element by index or by a field match.
func selectFrom(cur any, sel string) (any, bool) {
	arr, ok := cur.([]any)
	if !ok {
		return nil, false
	}
	if i, err := strconv.Atoi(sel); err == nil {
		if i < 0 || i >= len(arr) {
			return nil, false
		}
		return arr[i], true
	}
	field, want, found := strings.Cut(sel, "=")
	if !found {
		return nil, false
	}
	field, want = strings.TrimSpace(field), strings.TrimSpace(want)
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", m[field]) == want {
			return item, true
		}
	}
	return nil, false
}

// shortJSONError trims a transport error to something a card can show.
func shortJSONError(msg string) string {
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "the device did not answer in time"
	case strings.Contains(msg, "refused"):
		return "connection refused"
	case strings.Contains(msg, "no such host"):
		return "name not found"
	}
	return msg
}
