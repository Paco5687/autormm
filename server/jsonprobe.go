package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
)

// Reading values out of a device's own JSON.
//
// Plenty of hardware has no SNMP at all but does serve its state as JSON over
// HTTP — aquarium controllers, solar inverters, 3D printers, weather stations,
// anything built in the last decade with a web UI. A check that can only say
// "it answered" throws away everything interesting about those.
//
// So: fetch a URL, pull named values out of the response, and show them on the
// card like any other reading. Read-only: the status fetch is always a GET, and
// the one POST this makes is a login, because some of the interesting APIs are
// behind one — a UniFi controller holds the only copy of a smart PDU's
// per-outlet power, and will not part with it unauthenticated.

// JSONProbe is one value to pull out of a JSON response.
type JSONProbe struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Unit  string `json:"unit,omitempty"`
	// Max turns the reading into a bar rather than a figure. Zero leaves it a
	// figure, which is right for a temperature and wrong for a percentage.
	Max float64 `json:"max,omitempty"`
	// Good says which end of the scale is the healthy one: "high" for a signal
	// strength, a battery, a satisfaction score. Empty means low, which is right
	// for the readings a dashboard usually carries — processor, memory, disk —
	// and is why it is the default.
	//
	// Without this the colour is a guess dressed as a fact: a link quality of
	// 96% was drawn in the same alarming red as a disk at 96%, which is exactly
	// backwards and undermines the one thing a colour is for.
	Good string `json:"good,omitempty"`
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
	// Group names the set a reading belongs to, when one probe expanded into
	// many. The card draws those together and densely, because twenty outlets
	// listed the way two sensors are listed is a card nobody can read.
	Group string `json:"group,omitempty"`
	// GoodHigh flips which end of the bar is alarming.
	GoodHigh bool `json:"good_high,omitempty"`
}

// JSONAuth is how to authenticate to a status API.
type JSONAuth struct {
	// Mode is "", "basic", "bearer" or "login".
	Mode  string `json:"mode,omitempty"`
	User  string `json:"user,omitempty"`
	Pass  string `json:"pass,omitempty"`
	Token string `json:"token,omitempty"`
	// LoginURL and LoginBody drive the "login" mode: the body is posted as JSON
	// and whatever cookies come back are used for the status fetch. That covers
	// the shape almost every appliance controller uses.
	LoginURL  string `json:"login_url,omitempty"`
	LoginBody string `json:"login_body,omitempty"`
}

// sessions holds a cookie jar per check, so a login is not repeated on every
// poll. A controller that hands out a session and is then asked to issue
// another one every minute will eventually start refusing.
type sessions struct {
	mu  sync.Mutex
	jar map[string]http.CookieJar
}

func newSessions() *sessions { return &sessions{jar: map[string]http.CookieJar{}} }

func (s *sessions) get(id string) (http.CookieJar, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jar[id]
	return j, ok
}

func (s *sessions) set(id string, j http.CookieJar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jar[id] = j
}

func (s *sessions) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jar, id)
}

// probeJSONAuth is probeJSON with credentials, retrying once through a fresh
// login when the API says the session is no longer good.
func probeJSONAuth(ctx context.Context, id, url string, probes []JSONProbe, auth JSONAuth, sess *sessions) ([]Reading, string) {
	readings, msg, unauthorised := probeOnce(ctx, id, url, probes, auth, sess)
	if unauthorised && auth.Mode == "login" {
		// The session expired, which is ordinary rather than an error: drop it
		// and log in again before giving up on the poll.
		sess.drop(id)
		readings, msg, _ = probeOnce(ctx, id, url, probes, auth, sess)
	}
	return readings, msg
}

// probeJSON fetches a URL and extracts the configured values.
//
// Errors are returned as a message rather than as an absence: a probe that
// stopped matching because the device changed its JSON should say so, not
// quietly show nothing.
func probeJSON(ctx context.Context, url string, probes []JSONProbe) ([]Reading, string) {
	r, msg, _ := probeOnce(ctx, "", url, probes, JSONAuth{}, nil)
	return r, msg
}

// probeOnce performs one attempt, reporting separately whether it failed for
// want of a valid session so the caller can decide to log in again.
func probeOnce(ctx context.Context, id, url string, probes []JSONProbe, auth JSONAuth, sess *sessions) ([]Reading, string, bool) {
	if url == "" || len(probes) == 0 {
		return nil, "", false
	}
	client, err := authedClient(ctx, id, auth, sess)
	if err != nil {
		return nil, "sign-in failed: " + shortJSONError(err.Error()), false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err.Error(), false
	}
	req.Header.Set("User-Agent", "autormm-healthcheck")
	applyRequestAuth(req, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, shortJSONError(err.Error()), false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Sprintf("HTTP %d — the API refused the credentials", resp.StatusCode), true
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Sprintf("HTTP %d from the device", resp.StatusCode), false
	}

	doc, err := decodeStatus(resp)
	if err != nil {
		return nil, err.Error(), false
	}

	var out []Reading
	var missing []string
	for _, p := range probes {
		// A path carrying a wildcard reads a whole table rather than one cell.
		if rs, isWild := expandProbe(doc, p); isWild {
			if len(rs) == 0 {
				missing = append(missing, p.Label)
				continue
			}
			out = append(out, rs...)
			continue
		}
		v, ok := jsonPath(doc, p.Path)
		if !ok {
			missing = append(missing, p.Label)
			continue
		}
		out = append(out, readingFrom(p, v))
	}
	if len(missing) > 0 {
		return out, "no value at the path for: " + strings.Join(missing, ", "), false
	}
	return out, "", false
}

// decodeStatus reads a status document off a response.
//
// Bounded: this is a status document, and a device answering with a gigabyte of
// something is not one.
func decodeStatus(resp *http.Response) (any, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("could not read the response")
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the response is not JSON")
	}
	return doc, nil
}

// fetchStatus performs the authenticated GET a probe would, and hands back the
// decoded document rather than readings — for the things that want the shape of
// the answer rather than a value out of it, such as a switch's port table.
func fetchStatus(ctx context.Context, id, url string, auth JSONAuth, sess *sessions) (any, error) {
	client, err := authedClient(ctx, id, auth, sess)
	if err != nil {
		return nil, fmt.Errorf("sign-in failed: %s", shortJSONError(err.Error()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "autormm-healthcheck")
	applyRequestAuth(req, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", shortJSONError(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// The held session may simply have expired; drop it so the next attempt
		// signs in again rather than failing the same way for ever.
		if auth.Mode == "login" && sess != nil {
			sess.drop(id)
		}
		return nil, fmt.Errorf("HTTP %d — the API refused the credentials", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from the device", resp.StatusCode)
	}
	return decodeStatus(resp)
}

// applyRequestAuth adds whatever goes on the request itself.
func applyRequestAuth(req *http.Request, auth JSONAuth) {
	switch auth.Mode {
	case "basic":
		req.SetBasicAuth(auth.User, auth.Pass)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	}
}

// authedClient returns a client carrying a session, logging in first if this
// API needs one and no usable session is held.
func authedClient(ctx context.Context, id string, auth JSONAuth, sess *sessions) (*http.Client, error) {
	if auth.Mode != "login" || sess == nil {
		return appClient, nil
	}
	if jar, ok := sess.get(id); ok {
		return clientWithJar(jar), nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := clientWithJar(jar)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, auth.LoginURL,
		strings.NewReader(loginBody(auth)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "autormm-healthcheck")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sess.set(id, jar)
	return client, nil
}

// loginBody is what gets posted to sign in.
//
// Built from the username and password: a controller wants
// {"username":…,"password":…} and asking an operator to type that as JSON is
// asking them to get a quote wrong at the end of a long day — and to retype a
// password every time they touch anything else on the device, since a password
// inside a blob cannot be kept the way a password field is.
//
// Encoded rather than concatenated. A password is allowed to contain a quote or
// a backslash, and a hand-built string would produce a malformed body that
// fails as an authentication error — the one error message guaranteed to send
// someone looking at their credentials rather than at this.
func loginBody(auth JSONAuth) string {
	// A username wins over a written-out body. That ordering is what lets a
	// device configured the old way be moved to the fields without having to
	// clear anything first: filling them in is the whole gesture. The body is
	// still there for an API that names its fields something else, which is the
	// only reason it existed.
	if strings.TrimSpace(auth.User) == "" {
		return strings.TrimSpace(auth.LoginBody)
	}
	b, err := json.Marshal(map[string]string{
		"username": auth.User,
		"password": auth.Pass,
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// clientWithJar mirrors appClient's settings — self-signed certificates are the
// norm on this gear — but follows redirects, which a login flow depends on.
func clientWithJar(jar http.CookieJar) *http.Client {
	return &http.Client{
		Timeout:   checkTimeout,
		Jar:       jar,
		Transport: appClient.Transport,
	}
}

// readingFrom renders one extracted value.
func readingFrom(p JSONProbe, v any) Reading {
	r := Reading{Label: p.Label, Unit: p.Unit, Max: p.Max,
		GoodHigh: strings.EqualFold(strings.TrimSpace(p.Good), "high")}
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
	r.Text = humanNumber(r.Num, p.Max) + p.Unit
	return r
}

// humanNumber prints a figure at the size a person reads it at.
//
// Device counters are raw: a switch port's throughput arrives as bytes per
// second, its lifetime traffic as a plain count of bytes. Printed literally
// that is "622343.54B/s" and "1708467592509B" — technically exact, and on a
// card at ten point it is a wall of digits nobody reads. Three significant
// figures and an SI prefix says the same thing.
//
// Not applied to anything with a full scale: that is drawn as a bar, where the
// number beside it is a percentage and belongs exact.
func humanNumber(f, max float64) string {
	if max > 0 {
		return trimFloat(f)
	}
	abs := math.Abs(f)
	if abs < 10000 {
		return trimFloat(f)
	}
	units := []string{"k", "M", "G", "T", "P"}
	for i, u := range units {
		abs /= 1000
		if abs < 1000 || i == len(units)-1 {
			v := f / math.Pow(1000, float64(i+1))
			// Three significant figures: 1.71T, 62.2k, 622k — enough to compare
			// two ports at a glance, which is what the number is for.
			switch {
			case math.Abs(v) < 10:
				return strconv.FormatFloat(v, 'f', 2, 64) + u
			case math.Abs(v) < 100:
				return strconv.FormatFloat(v, 'f', 1, 64) + u
			default:
				return strconv.FormatFloat(v, 'f', 0, 64) + u
			}
		}
	}
	return trimFloat(f)
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
			// A bare number against an array is an index. Checked before the
			// map lookup, because "heads.0.extruders" is the documented form
			// and it is not a key named "0".
			if arr, isArr := cur.([]any); isArr && !hasSel {
				i, err := strconv.Atoi(key)
				if err != nil || i < 0 || i >= len(arr) {
					return nil, false
				}
				cur = arr[i]
				continue
			}
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

// expandProbe reads a whole table through one probe.
//
// A selector of the form [*name] means "every entry, labelled by its name
// field" rather than "the one entry whose name is this". It exists because the
// alternative does not scale: a PDU has twenty outlets, and writing twenty
// near-identical lines to read them is both tedious and a standing invitation
// to get one of them subtly wrong.
//
// The second return says whether the path had a wildcard at all, which is what
// separates "this reads a table and the table was empty" from "this reads one
// value and should be looked up the ordinary way".
func expandProbe(doc any, p JSONProbe) ([]Reading, bool) {
	segs := strings.Split(p.Path, ".")
	at, field := -1, ""
	for i, seg := range segs {
		key, sel, hasSel := cutSelector(strings.TrimSpace(seg))
		if hasSel && strings.HasPrefix(sel, "*") {
			at, field = i, strings.TrimSpace(sel[1:])
			segs[i] = key
			break
		}
	}
	if at < 0 {
		return nil, false
	}
	// Everything up to and including the wildcard segment locates the table;
	// everything after it locates the value within one row.
	table, ok := jsonPath(doc, strings.Join(segs[:at+1], "."))
	if !ok {
		return nil, true
	}
	rows, ok := table.([]any)
	if !ok {
		return nil, true
	}
	rest := strings.Join(segs[at+1:], ".")

	out := make([]Reading, 0, len(rows))
	for i, row := range rows {
		v := row
		if rest != "" {
			var found bool
			if v, found = jsonPath(row, rest); !found {
				// A row that does not carry this value is skipped rather than
				// shown as blank: a PDU's USB outlets report no wattage at all,
				// and listing them empty says less than not listing them.
				continue
			}
		}
		label := ""
		if m, isMap := row.(map[string]any); isMap && field != "" {
			if lv, has := m[field]; has {
				label = strings.TrimSpace(fmt.Sprintf("%v", lv))
			}
		}
		if label == "" {
			label = strconv.Itoa(i + 1)
		}
		r := readingFrom(p, v)
		r.Label, r.Group = label, p.Label
		out = append(out, r)
	}
	return out, true
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
