package server

import "testing"

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
