package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

// The case that sent the operator to a blank page: a device that refuses the
// first port scanned and serves its interface on a later one. Stopping at the
// refusal recorded it as up on the refused port, and the link was built from
// that port number.
func TestScanKeepsLookingPastARefusedPort(t *testing.T) {
	// A listener we close immediately gives us a port that is certain to
	// refuse, on a host that is certainly alive.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := portOf(t, dead.Addr().String())
	dead.Close()

	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	livePort := portOf(t, live.Addr().String())

	saved := commonPorts
	commonPorts = []int{deadPort, livePort}
	defer func() { commonPorts = saved }()

	up, _, answered, err := probe(context.Background(), "127.0.0.1", 0)
	if !up || err != nil {
		t.Fatalf("host reported down: %v", err)
	}
	if answered != livePort {
		t.Errorf("answered = %d, want the port that actually listens (%d)", answered, livePort)
	}
}

// Only refusals still means the host is up — that distinction is what keeps a
// wrong port guess from paging the operator.
func TestOnlyRefusalsStillMeansUp(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	p := portOf(t, ln.Addr().String())
	ln.Close()

	saved := commonPorts
	commonPorts = []int{p}
	defer func() { commonPorts = saved }()

	up, _, answered, err := probe(context.Background(), "127.0.0.1", 0)
	if !up || err != nil {
		t.Fatalf("a refusing host reported down: %v", err)
	}
	if answered != 0 {
		t.Errorf("answered = %d, want 0 — nothing accepted a connection", answered)
	}
}

// A device serving TLS is linked over https even though its port is not one of
// the ports conventionally read as https.
func TestHTTPSDeviceOnAnOddPortIsLinkedOverHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // a login page is a healthy device
	}))
	defer srv.Close()
	p := portOf(t, srv.Listener.Addr().String())

	c := NetCheck{Address: "127.0.0.1", Port: p}
	got := deviceWebURL(context.Background(), c, p)
	want := "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
	if got != want {
		t.Errorf("web URL = %q, want %q", got, want)
	}
	// And the guess it replaces would have been wrong.
	if c.WebURL() == want {
		t.Log("note: the port guess happened to agree here")
	}
}

// A plain-http device is still linked over http, and is not left with an https
// link that will not load.
func TestPlainHTTPDeviceIsLinkedOverHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	p := portOf(t, srv.Listener.Addr().String())

	got := deviceWebURL(context.Background(), NetCheck{Address: "127.0.0.1", Port: p}, p)
	want := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
	if got != want {
		t.Errorf("web URL = %q, want %q", got, want)
	}
}

// A self-signed certificate is the norm on homelab appliances. Refusing to look
// would leave every one of them without a working link.
func TestSelfSignedCertificateStillLinks(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{}
	srv.StartTLS()
	defer srv.Close()
	p := portOf(t, srv.Listener.Addr().String())

	if got := deviceWebURL(context.Background(), NetCheck{Address: "127.0.0.1", Port: p}, p); got == "" {
		t.Error("no link for a device with a self-signed certificate")
	}
}

// Ports that are not web interfaces give no link, rather than one that opens a
// browser error.
func TestNonWebPortsGiveNoLink(t *testing.T) {
	for _, p := range []int{22, 445, 9100, 139, 3389} {
		if got := deviceCandidates(NetCheck{Address: "192.0.2.10", Port: p}, p); got != nil {
			t.Errorf("port %d offered %v", p, got)
		}
	}
}

// Default ports are left off the link, so it reads the way it would be typed.
func TestDefaultPortsAreNotWrittenOut(t *testing.T) {
	if got := urlFor("https", "192.0.2.10", 443); got != "https://192.0.2.10" {
		t.Errorf("got %q", got)
	}
	if got := urlFor("http", "192.0.2.10", 80); got != "http://192.0.2.10" {
		t.Errorf("got %q", got)
	}
	if got := urlFor("http", "192.0.2.10", 443); got != "http://192.0.2.10:443" {
		t.Errorf("got %q", got)
	}
}
