package server

import "testing"

func TestCheckBindSafety(t *testing.T) {
	cases := []struct {
		addr        string
		allowPublic bool
		wantErr     bool
	}{
		{"192.168.1.10:8765", false, false},
		{"10.0.0.5:8765", false, false},
		{"192.168.99.1:8765", false, false},
		{"127.0.0.1:8765", false, false},
		{"100.64.0.3:8765", false, false},            // CGNAT / Tailscale
		{"hub.local:8765", false, false},             // hostname — can't classify, allow
		{"8.8.8.8:8765", false, true},                // public — refuse
		{"8.8.8.8:8765", true, false},                // public but explicitly allowed
		{"[2606:4700:4700::1111]:8765", false, true}, // public IPv6 — refuse
	}
	for _, c := range cases {
		warned := false
		err := CheckBindSafety(c.addr, c.allowPublic, func(string) { warned = true })
		if (err != nil) != c.wantErr {
			t.Errorf("%s allowPublic=%v: err=%v, wantErr=%v", c.addr, c.allowPublic, err, c.wantErr)
		}
		if c.addr == "8.8.8.8:8765" && c.allowPublic && !warned {
			t.Errorf("expected a warning when allowing a public bind")
		}
	}
}
