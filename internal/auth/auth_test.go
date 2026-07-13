package auth

import (
	"testing"
	"time"
)

func TestTicketRoundTrip(t *testing.T) {
	secret := DeriveSecret("hunter2")
	tok := SignTicket(secret, "sess-abc", "host-1", time.Minute)
	tkt, err := VerifyTicket(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if tkt.Session != "sess-abc" || tkt.AgentID != "host-1" {
		t.Fatalf("bad ticket: %+v", tkt)
	}
}

func TestTicketRejectsTamperAndExpiry(t *testing.T) {
	secret := DeriveSecret("hunter2")
	other := DeriveSecret("different")

	tok := SignTicket(secret, "s", "h", time.Minute)
	if _, err := VerifyTicket(other, tok); err == nil {
		t.Fatal("expected signature failure under wrong secret")
	}

	expired := SignTicket(secret, "s", "h", -time.Second)
	if _, err := VerifyTicket(secret, expired); err == nil {
		t.Fatal("expected expiry failure")
	}
}

func TestTokenEqual(t *testing.T) {
	if !TokenEqual("abc", "abc") {
		t.Fatal("equal tokens should match")
	}
	if TokenEqual("abc", "abd") || TokenEqual("", "") {
		t.Fatal("unequal or empty tokens must not match")
	}
}
