package server

import "testing"

// systemd-resolved synthesises "_gateway" for the default route, so a resolver
// that knows nothing about the LAN still answers the router's reverse lookup
// with it. Found on a real network, where it was the only "name" returned.
func TestSyntheticNamesAreRejected(t *testing.T) {
	for _, n := range []string{"_gateway", "gateway", "localhost", "localhost.localdomain", "unknown", "-", "  _gateway  "} {
		if got := tidyHostname(n); got != "" {
			t.Errorf("%q was accepted as a name: %q", n, got)
		}
	}
	// An address is not a name either.
	if got := tidyHostname("192.0.2.44"); got != "" {
		t.Errorf("an address was accepted as a name: %q", got)
	}
}

func TestLocalSuffixesAreTrimmed(t *testing.T) {
	for in, want := range map[string]string{
		"printer01.local.": "printer01",
		"nas.lan":          "nas",
		"pi.home":          "pi",
		"switch01":         "switch01",
		"host.example.com": "host.example.com", // a real domain is left alone
	} {
		if got := tidyHostname(in); got != want {
			t.Errorf("tidyHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// A locally administered address was made up by a hypervisor, a container
// runtime or a phone randomising itself; reporting a vendor would invent one.
func TestLocallyAdministeredMACsHaveNoVendor(t *testing.T) {
	// Addresses no registry and no hypervisor claims — the ones that really are
	// invented. Docker and QEMU have their own ranges and are named instead.
	for _, mac := range []string{"fe:29:c1:25:90:ee", "9a:7c:5b:97:cc:12", "ae:11:22:33:44:55"} {
		if got := macVendor(mac); got != "randomised" {
			t.Errorf("macVendor(%q) = %q, want randomised", mac, got)
		}
	}
	// A real assignment is reported.
	if got := macVendor("bc:24:11:6d:d4:de"); got != "Proxmox" {
		t.Errorf("got %q, want Proxmox", got)
	}
	if got := macVendor("b8:27:eb:00:11:22"); got != "Raspberry Pi" {
		t.Errorf("got %q, want Raspberry Pi", got)
	}
	// A globally administered prefix nobody registered says nothing rather than
	// guessing.
	if got := macVendor("ff:ff:ff:ff:ff:ff"); got != "randomised" && got != "" {
		t.Errorf("invented a vendor: %q", got)
	}
	if got := macVendor("short"); got != "" {
		t.Errorf("parsed nonsense: %q", got)
	}
}

// The reverse-lookup name is built least-significant octet first.
func TestArpaNameOrder(t *testing.T) {
	if got := arpaName([]byte{192, 0, 2, 44}); got != "44.2.0.192.in-addr.arpa." {
		t.Errorf("got %q", got)
	}
	if got := arpaName([]byte{10, 0, 0, 1}); got != "1.0.0.10.in-addr.arpa." {
		t.Errorf("got %q", got)
	}
}

// A short reply must not be read past its end.
func TestNetbiosParserRejectsShortReplies(t *testing.T) {
	for _, b := range [][]byte{nil, make([]byte, 10), make([]byte, 56), make([]byte, 57)} {
		if got := parseNetbiosNames(b); got != "" {
			t.Errorf("parsed %d bytes into %q", len(b), got)
		}
	}
	// A reply claiming more entries than it carries stops at the end.
	short := make([]byte, 57)
	short[56] = 99 // count
	if got := parseNetbiosNames(short); got != "" {
		t.Errorf("trusted a lying count: %q", got)
	}
}

// A hypervisor invents its addresses rather than buying a registry assignment,
// so those ranges never appear in the IEEE data. Calling a virtual machine
// "randomised" throws away something worth knowing.
func TestVirtualPrefixesBeatTheRandomisedFallback(t *testing.T) {
	for mac, want := range map[string]string{
		"52:54:00:12:34:56": "QEMU/KVM",
		"02:42:ac:11:00:02": "Docker",
		"00:16:3e:aa:bb:cc": "Xen",
	} {
		if got := macVendor(mac); got != want {
			t.Errorf("macVendor(%q) = %q, want %q", mac, got, want)
		}
	}
	// Anything else locally administered genuinely is made up.
	if got := macVendor("fe:29:c1:25:90:ee"); got != "randomised" {
		t.Errorf("got %q, want randomised", got)
	}
}

// The generated table is the point of generating it: the hand-written one
// carried nine Espressif prefixes out of the hundreds that exist, and so failed
// to name an ESP32 device.
func TestGeneratedTableCoversRealAssignments(t *testing.T) {
	for mac, want := range map[string]string{
		"ec:c9:ff:96:d3:48": "Espressif", // found unnamed on a real network
		"bc:24:11:6d:d4:de": "Proxmox",
		"b8:27:eb:00:11:22": "Raspberry Pi",
	} {
		if got := macVendor(mac); got != want {
			t.Errorf("macVendor(%q) = %q, want %q", mac, got, want)
		}
	}
	if len(ouiVendors) < 5000 {
		t.Errorf("only %d prefixes; the table looks hand-written again", len(ouiVendors))
	}
}
