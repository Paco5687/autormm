package server

import "strings"

// Vendor names from MAC prefixes.
//
// Not a name, but the thing that makes an unnamed device identifiable: an
// address with "Ubiquiti" or "Espressif" beside it is something you can go and
// look for. It also needs no network at all, so it is the one identification
// that always works — a device that answers nothing still has a MAC.
//
// The table itself is generated from the IEEE registry; see macvendor_oui.go.

// virtualPrefixes names the locally administered ranges that do belong to
// something identifiable. A hypervisor picks its own addresses rather than
// buying a registry assignment, so these never appear in the IEEE data, and
// calling a virtual machine "randomised" throws away something worth knowing.
var virtualPrefixes = map[string]string{
	"52:54:00": "QEMU/KVM",
	"00:16:3e": "Xen",
	"0a:00:27": "VirtualBox",
	"02:42":    "Docker", // Docker composes its own from a fixed two-octet stem
	"02:50:41": "Parallels",
}

// macVendor names whoever was assigned this MAC's prefix.
//
// Order matters. A locally administered address — the second-least-significant
// bit of the first octet — belongs to no registry, but some of those ranges are
// still identifiable: a hypervisor invents its addresses rather than buying an
// assignment. So known prefixes are checked first, and only what is left over
// is reported as randomised, which is what a container runtime or a phone
// rotating its address actually is.
func macVendor(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if len(mac) < 8 {
		return ""
	}
	if v, ok := virtualPrefixes[mac[:8]]; ok {
		return v
	}
	if v, ok := virtualPrefixes[mac[:5]]; ok {
		return v
	}
	if v, ok := ouiVendors[mac[:8]]; ok {
		return v
	}
	if b, ok := firstOctet(mac); ok && b&0x02 != 0 {
		return "randomised"
	}
	return ""
}

// firstOctet parses the leading hex pair of a MAC.
func firstOctet(mac string) (byte, bool) {
	if len(mac) < 2 {
		return 0, false
	}
	var v byte
	for i := 0; i < 2; i++ {
		c := mac[i]
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | (c - '0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | (c - 'a' + 10)
		default:
			return 0, false
		}
	}
	return v, true
}
