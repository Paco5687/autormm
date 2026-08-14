package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTLSTargetOnlyAppliesToHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"https://nas.lan:5001/admin":  "nas.lan:5001",
		"https://10.0.0.5":            "10.0.0.5:443",
		"http://printer.lan":          "", // no certificate to read, and absence is not expiry
		"":                            "",
		"not a url at all":            "",
		"https://unifi.lan:8443/api/": "unifi.lan:8443",
	} {
		if got, _ := tlsTarget(in); got != want {
			t.Errorf("tlsTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// The real thing end to end: a TLS server, one handshake, the leaf's date.
func TestPeekCertReadsTheLeaf(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	hostport, sni := tlsTarget(srv.URL)
	info := peekCert(context.Background(), hostport, sni)
	if info == nil {
		t.Fatal("no certificate read")
	}
	want := srv.Certificate().NotAfter.Unix()
	if info.ExpiresUnix != want {
		t.Errorf("expiry %d, want %d", info.ExpiresUnix, want)
	}
}

func selfSigned(t *testing.T, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nas.lan"},
		Issuer:       pkix.Name{CommonName: "nas.lan"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// "Expiring" and "expiring and self-signed" are different news: the first is
// fixed by a renewal that may be automated, the second by whoever made the
// certificate, which is probably you.
func TestSelfSignedIsSaidPlainly(t *testing.T) {
	info := certInfoOf(selfSigned(t, time.Now().Add(10*24*time.Hour)))
	if !info.SelfSigned {
		t.Error("a certificate that signed itself was not marked self-signed")
	}
	if info.Issuer != "nas.lan" {
		t.Errorf("issuer = %q", info.Issuer)
	}
}

// The date changes on renewal and no faster; the cache is what keeps this from
// being ten thousand handshakes per certificate per renewal.
func TestCertCacheReadsSlowly(t *testing.T) {
	calls := 0
	cc := newCertCache()
	cc.peek = func(ctx context.Context, hostport, sni string) *CertInfo {
		calls++
		return &CertInfo{ExpiresUnix: 123}
	}
	for i := 0; i < 50; i++ {
		if got := cc.get(context.Background(), "dev1", "https://nas.lan"); got == nil || got.ExpiresUnix != 123 {
			t.Fatalf("read %d: %+v", i, got)
		}
	}
	if calls != 1 {
		t.Errorf("%d handshakes for 50 checks; the cache is not caching", calls)
	}
	// A URL this does not apply to costs nothing and answers nothing.
	if cc.get(context.Background(), "dev2", "http://printer.lan") != nil {
		t.Error("an http URL produced a certificate")
	}
	if calls != 1 {
		t.Error("an http URL cost a handshake")
	}
}

// One failed handshake must not blank a known date: the certificate did not
// stop existing because the device dropped a packet.
func TestAFailedReadKeepsThePreviousAnswer(t *testing.T) {
	cc := newCertCache()
	good := &CertInfo{ExpiresUnix: 456, Issuer: "R11"}
	cc.peek = func(ctx context.Context, hostport, sni string) *CertInfo { return good }
	cc.get(context.Background(), "dev", "https://nas.lan")

	// Age the entry out, then fail the re-read.
	cc.mu.Lock()
	cc.m["dev"] = certEntry{info: good, at: time.Now().Add(-2 * certRecheck)}
	cc.mu.Unlock()
	cc.peek = func(ctx context.Context, hostport, sni string) *CertInfo { return nil }
	if got := cc.get(context.Background(), "dev", "https://nas.lan"); got == nil || got.ExpiresUnix != 456 {
		t.Errorf("the known date was blanked by one failed handshake: %+v", got)
	}
}
