package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebURLInferredFromPort(t *testing.T) {
	for _, tc := range []struct {
		port int
		want string
	}{
		{0, "http://192.0.2.10"},          // unknown: the probe tries several, http is the fair guess
		{80, "http://192.0.2.10"},         // the scheme's own port is left off
		{443, "https://192.0.2.10"},       //
		{8006, "https://192.0.2.10:8006"}, // Proxmox
		{8443, "https://192.0.2.10:8443"},
		{8080, "http://192.0.2.10:8080"},
	} {
		c := NetCheck{Address: "192.0.2.10", Port: tc.port}
		if got := c.WebURL(); got != tc.want {
			t.Errorf("port %d → %q, want %q", tc.port, got, tc.want)
		}
	}
}

// Ports that are not web interfaces must yield no link. Sending someone to a
// browser error is worse than leaving the card inert — a printer's raw port or
// an SSH daemon has no control panel to open.
func TestWebURLIsEmptyForNonWebPorts(t *testing.T) {
	for _, port := range []int{22, 445, 9100, 139, 3389} {
		c := NetCheck{Address: "192.0.2.10", Port: port}
		if got := c.WebURL(); got != "" {
			t.Errorf("port %d produced a link %q; it has no web interface", port, got)
		}
	}
}

// An explicit URL always wins: plenty of devices answer on one port and serve
// their interface on another, or need a path.
func TestExplicitURLOverridesInference(t *testing.T) {
	c := NetCheck{Address: "192.0.2.10", Port: 9100, URL: "http://192.0.2.10/printer/status"}
	if got := c.WebURL(); got != "http://192.0.2.10/printer/status" {
		t.Errorf("explicit URL was not used: %q", got)
	}
}

// IPv6 literals must come back bracketed, or the link is malformed.
func TestWebURLBracketsIPv6(t *testing.T) {
	c := NetCheck{Address: "2001:db8::1", Port: 8080}
	if got := c.WebURL(); got != "http://[2001:db8::1]:8080" {
		t.Errorf("IPv6 address not bracketed: %q", got)
	}
}

// An app is reachable if it answers at all. 401 and 403 mean it is running and
// asking for credentials, which is how most self-hosted things behave behind a
// login — reporting those as down would mark a healthy homelab as broken.
func TestAppProbeTreatsAnyResponseAsReachable(t *testing.T) {
	for _, code := range []int{200, 204, 302, 401, 403, 500, 502} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		up, _, got, _, err := probeApp(context.Background(), srv.URL)
		srv.Close()
		if !up || err != nil {
			t.Errorf("status %d reported unreachable (err=%v)", code, err)
		}
		if got != code {
			t.Errorf("status reported as %d, want %d", got, code)
		}
	}
}

// Nothing listening is the only thing that means down.
func TestAppProbeDownWhenNothingAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now refused
	if up, _, _, _, err := probeApp(context.Background(), url); up || err == nil {
		t.Error("a closed server was reported as reachable")
	}
}

// Redirects are not followed: a 302 to a login page is a healthy app, and
// chasing it risks wandering off to an identity provider entirely.
func TestAppProbeDoesNotFollowRedirects(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()
	_, _, code, _, _ := probeApp(context.Background(), srv.URL)
	if code != http.StatusFound {
		t.Errorf("code = %d, want 302 (the redirect itself)", code)
	}
	if hits != 1 {
		t.Errorf("made %d requests; the redirect was followed", hits)
	}
}

// Self-signed certificates are the norm for homelab apps. Verifying would
// report every one of them as down while proving nothing — this checks
// liveness and trusts none of what it receives.
func TestAppProbeAcceptsSelfSignedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if up, _, _, _, err := probeApp(context.Background(), srv.URL); !up {
		t.Errorf("a self-signed app was reported down: %v", err)
	}
}

// An app entry with no URL cannot be checked, and must say so rather than
// silently reporting down.
func TestAppWithoutURLErrors(t *testing.T) {
	_, _, _, _, err := probeCheck(context.Background(), NetCheck{Kind: "app", Name: "sonarr"})
	if err == nil {
		t.Error("an app with no URL did not report why it could not be checked")
	}
}

// An app is identified by its URL, a device by its address. Requiring both of
// each would reject perfectly ordinary entries of either kind.
func TestAppWebURLUsesItsURLVerbatim(t *testing.T) {
	c := NetCheck{Kind: "app", Name: "Jellyfin", URL: "https://jellyfin.example.com/web/"}
	if got := c.WebURL(); got != "https://jellyfin.example.com/web/" {
		t.Errorf("app URL mangled: %q", got)
	}
	if !c.IsApp() {
		t.Error("kind app not recognised")
	}
}

// An app written without a scheme must not be assumed to be http. Trying https
// first costs a retry when it is wrong; assuming http marks an https-only app
// as down, which is the failure that prompted this.
func TestSchemelessAppPrefersHTTPS(t *testing.T) {
	got := appCandidates("jellyfin.example.com")
	want := []string{"https://jellyfin.example.com", "http://jellyfin.example.com"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// A scheme the operator supplied is respected absolutely — never second-guessed
// by trying the other one.
func TestExplicitSchemeIsNotSecondGuessed(t *testing.T) {
	for _, u := range []string{"http://box.example.com:8096", "https://box.example.com"} {
		if got := appCandidates(u); len(got) != 1 || got[0] != u {
			t.Errorf("appCandidates(%q) = %v, want exactly [%q]", u, got, u)
		}
	}
}

// The scheme that answered is what the card must link to, so an https-only app
// entered without a scheme opens over https.
func TestProbeReportsTheSchemeThatAnswered(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	bare := strings.TrimPrefix(srv.URL, "https://")
	up, _, _, used, err := probeApp(context.Background(), bare)
	if !up {
		t.Fatalf("https-only app reported down: %v", err)
	}
	if used != srv.URL {
		t.Errorf("answered URL = %q, want %q", used, srv.URL)
	}
}

// And a schemeless http-only app still resolves, after https fails.
func TestSchemelessFallsBackToHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	bare := strings.TrimPrefix(srv.URL, "http://")
	up, _, _, used, err := probeApp(context.Background(), bare)
	if !up {
		t.Fatalf("http-only app reported down: %v", err)
	}
	if used != srv.URL {
		t.Errorf("answered URL = %q, want %q", used, srv.URL)
	}
}
