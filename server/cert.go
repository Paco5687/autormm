package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"sync"
	"time"
)

// Certificate expiry on agentless checks.
//
// A homelab runs on HTTPS services, and a certificate that quietly expires
// takes one down at a moment nobody chose. The hub already connects to these
// devices over TLS on every check; the expiry date is sitting in the handshake
// it is already doing. This reads it and says so before it matters.

// CertInfo is what a card needs to know about a device's certificate.
type CertInfo struct {
	ExpiresUnix int64  `json:"expires_unix"`
	Issuer      string `json:"issuer,omitempty"`
	// SelfSigned distinguishes "expiring" from "expiring and self-signed":
	// the first is fixed by a renewal that may even be automated, the second
	// by whoever made the certificate, which is probably you.
	SelfSigned bool `json:"self_signed,omitempty"`
}

// certRecheck is how often a certificate is actually read. It changes on
// renewal and no faster, so probing it on every 60-second check would be
// nearly ten thousand handshakes per certificate per renewal.
const certRecheck = 6 * time.Hour

type certEntry struct {
	info *CertInfo
	at   time.Time
}

// certCache remembers what each check's certificate said, and when.
type certCache struct {
	mu sync.Mutex
	m  map[string]certEntry
	// peek is swappable so the cache's behaviour is testable without minting
	// certificates for every case.
	peek func(ctx context.Context, hostport, sni string) *CertInfo
}

func newCertCache() *certCache {
	return &certCache{m: map[string]certEntry{}, peek: peekCert}
}

// get returns the certificate state for a check, reading it afresh only when
// the cached answer has aged out.
func (cc *certCache) get(ctx context.Context, id, httpsURL string) *CertInfo {
	hostport, sni := tlsTarget(httpsURL)
	if hostport == "" {
		return nil
	}
	cc.mu.Lock()
	e, ok := cc.m[id]
	cc.mu.Unlock()
	if ok && time.Since(e.at) < certRecheck {
		return e.info
	}
	info := cc.peek(ctx, hostport, sni)
	cc.mu.Lock()
	// A failed read keeps the previous answer rather than blanking it: the
	// certificate did not stop existing because one handshake timed out.
	if info == nil && ok {
		info = e.info
	}
	cc.m[id] = certEntry{info: info, at: time.Now()}
	cc.mu.Unlock()
	return info
}

func (cc *certCache) forget(id string) {
	cc.mu.Lock()
	delete(cc.m, id)
	cc.mu.Unlock()
}

// tlsTarget extracts where to handshake from a URL, if it is one this applies
// to at all. Only https: an http URL has no certificate to read, and absence
// of one is not an expiring one.
func tlsTarget(raw string) (hostport, sni string) {
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), u.Hostname()
}

// peekCert performs one handshake and reads the leaf.
//
// Verification is deliberately off. Self-signed certificates are the norm on
// this gear, and the question here is the date, not trust — turning
// verification on would break every device in the rack to answer a question
// nobody asked.
func peekCert(ctx context.Context, hostport, sni string) *CertInfo {
	d := &tls.Dialer{Config: &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         sni,
	}}
	dctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", hostport)
	if err != nil {
		return nil
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	return certInfoOf(state.PeerCertificates[0])
}

// certInfoOf summarises one certificate.
func certInfoOf(leaf *x509.Certificate) *CertInfo {
	issuer := leaf.Issuer.CommonName
	if issuer == "" && len(leaf.Issuer.Organization) > 0 {
		issuer = leaf.Issuer.Organization[0]
	}
	return &CertInfo{
		ExpiresUnix: leaf.NotAfter.Unix(),
		Issuer:      issuer,
		// Comparing the raw names rather than parsed fields: two distinct
		// parties can render to the same pretty string, but not to the same
		// bytes.
		SelfSigned: string(leaf.RawIssuer) == string(leaf.RawSubject),
	}
}
