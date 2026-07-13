// Package auth provides constant-time token comparison and short-lived,
// HMAC-signed session tickets for authorising media sockets.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// TokenEqual reports whether two tokens match, in constant time.
func TokenEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RandomID returns a URL-safe random identifier of n bytes of entropy.
func RandomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Ticket binds a session id + agent id with an expiry, signed with the server
// secret. Format: base64url(sid) "." base64url(agent) "." expUnix "." sig
type Ticket struct {
	Session string
	AgentID string
	Expires time.Time
}

// SignTicket produces a signed ticket string valid for ttl.
func SignTicket(secret []byte, session, agentID string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := ticketPayload(session, agentID, exp)
	sig := sign(secret, payload)
	return payload + "." + sig
}

// VerifyTicket validates signature + expiry and returns the ticket.
func VerifyTicket(secret []byte, tok string) (*Ticket, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 4 {
		return nil, fmt.Errorf("auth: malformed ticket")
	}
	payload := strings.Join(parts[:3], ".")
	want := sign(secret, payload)
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[3])) != 1 {
		return nil, fmt.Errorf("auth: bad signature")
	}
	sid, err := b64dec(parts[0])
	if err != nil {
		return nil, err
	}
	agent, err := b64dec(parts[1])
	if err != nil {
		return nil, err
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[2], "%d", &exp); err != nil {
		return nil, fmt.Errorf("auth: bad expiry")
	}
	if time.Now().Unix() > exp {
		return nil, fmt.Errorf("auth: ticket expired")
	}
	return &Ticket{Session: sid, AgentID: agent, Expires: time.Unix(exp, 0)}, nil
}

func ticketPayload(session, agentID string, exp int64) string {
	return b64enc(session) + "." + b64enc(agentID) + "." + fmt.Sprintf("%d", exp)
}

func sign(secret []byte, payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func b64enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func b64dec(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return string(b), err
}

// DeriveSecret expands a human secret into a 32-byte key.
func DeriveSecret(s string) []byte {
	sum := sha256.Sum256([]byte("autormm-v1:" + s))
	return sum[:]
}
