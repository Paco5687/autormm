package server

import "strings"

// Vendor names from MAC prefixes.
//
// Not a name, but the thing that makes an unnamed device identifiable: an
// address with "Ubiquiti" or "Raspberry Pi" beside it is something you can go
// and look for. It also needs no network at all, so it is the one identification
// that always works — a device that answers nothing still has a MAC.
//
// A curated list rather than the full IEEE registry, which is around thirty
// thousand entries and several megabytes. Vendor names are also checked against
// the release safety scan, which is how one of them was dropped: it contained a
// substring the scan treats as a host identifier, and loosening the scan to keep
// a defunct storage brand would have been the wrong way round. These are the assignments that turn
// up on a homelab; anything else simply has no vendor shown, which is the same
// as today.
var ouiVendors = map[string]string{
	// Network gear
	"00:15:6d": "Ubiquiti", "24:a4:3c": "Ubiquiti", "44:d9:e7": "Ubiquiti",
	"68:72:51": "Ubiquiti", "78:8a:20": "Ubiquiti", "80:2a:a8": "Ubiquiti",
	"b4:fb:e4": "Ubiquiti", "dc:9f:db": "Ubiquiti", "f0:9f:c2": "Ubiquiti",
	"e0:63:da": "Ubiquiti", "74:83:c2": "Ubiquiti", "d0:21:f9": "Ubiquiti",
	"00:0c:29": "VMware", "00:50:56": "VMware", "00:05:69": "VMware",
	"00:1c:14": "VMware", "00:1a:8c": "Cisco", "00:1b:0c": "Cisco",
	"00:0d:b9": "PC Engines", "00:e0:4c": "Realtek", "52:54:00": "QEMU/KVM",
	"08:00:27": "VirtualBox", "00:16:3e": "Xen", "bc:24:11": "Proxmox",
	"00:1d:7e": "Cisco-Linksys", "c0:56:27": "Belkin", "00:26:f2": "Netgear",
	"a0:40:a0": "Netgear", "9c:3d:cf": "Netgear", "20:4e:7f": "Netgear",
	"00:14:6c": "Netgear", "e4:f4:c6": "Netgear", "00:1f:33": "Netgear",
	"00:24:b2": "Netgear", "2c:30:33": "Netgear", "00:18:4d": "Netgear",
	"00:05:5d": "D-Link", "00:1e:58": "D-Link", "1c:bd:b9": "D-Link",
	"00:23:69": "Cisco-Linksys", "68:7f:74": "Cisco-Linksys",
	"00:1d:0f": "TP-Link", "14:cc:20": "TP-Link", "50:c7:bf": "TP-Link",
	"a4:2b:b0": "TP-Link", "c4:6e:1f": "TP-Link", "b0:be:76": "TP-Link",
	"00:0f:b5": "Netgear", "00:22:3f": "Netgear", "e0:91:f5": "Netgear",
	"00:90:4c": "Epigram", "00:13:46": "D-Link", "f4:f2:6d": "TP-Link",
	"70:4f:57": "MikroTik", "48:8f:5a": "MikroTik", "2c:c8:1b": "MikroTik",
	"64:d1:54": "MikroTik", "cc:2d:e0": "MikroTik", "dc:2c:6e": "MikroTik",
	"e4:8d:8c": "Routerboard", "00:0c:42": "Routerboard",

	// Single-board and small computers
	"b8:27:eb": "Raspberry Pi", "dc:a6:32": "Raspberry Pi", "e4:5f:01": "Raspberry Pi",
	"28:cd:c1": "Raspberry Pi", "2c:cf:67": "Raspberry Pi", "d8:3a:dd": "Raspberry Pi",
	"00:04:4b": "NVIDIA", "48:b0:2d": "NVIDIA", "00:c0:08": "Seiko",

	// Apple
	"00:1b:63": "Apple", "00:25:00": "Apple", "3c:07:54": "Apple",
	"a4:5e:60": "Apple", "ac:bc:32": "Apple", "b8:e8:56": "Apple",
	"f0:18:98": "Apple", "8c:85:90": "Apple", "dc:a9:04": "Apple",
	"7c:d1:c3": "Apple", "a8:66:7f": "Apple", "d0:81:7a": "Apple",

	// Printers
	"00:00:48": "Seiko Epson", "00:26:ab": "Seiko Epson", "a4:ee:57": "Seiko Epson",
	"00:80:77": "Brother", "00:1b:a9": "Brother", "30:05:5c": "Brother",
	"00:00:74": "Ricoh", "00:26:73": "Ricoh", "00:1e:0b": "HP",
	"00:1f:29": "HP", "3c:d9:2b": "HP", "94:57:a5": "HP", "b4:b5:2f": "HP",
	"00:15:99": "Samsung", "00:1c:c4": "HP", "70:5a:0f": "HP",
	"00:21:5a": "HP", "00:23:7d": "HP", "9c:b6:54": "HP",
	"08:00:37": "Fuji Xerox", "00:00:aa": "Xerox", "00:20:00": "Lexmark",
	"00:04:00": "Lexmark", "00:21:b7": "Lexmark", "e8:d8:d1": "HP",

	// Storage and UPS
	"00:11:32": "Synology", "00:1b:4f": "Synology", "90:09:d0": "Synology",
	"00:08:9b": "ICP Electronics", "24:5e:be": "QNAP",
	"00:c0:b7": "APC", "00:20:85": "APC", "28:29:86": "APC",
	"00:03:ea": "APC", "00:1e:67": "Intel", "00:04:d9": "Titan",
	"00:11:5b": "Eaton", "00:07:e9": "Intel",

	// Media and IoT
	"b8:27:35": "Google", "f4:f5:d8": "Google", "54:60:09": "Google",
	"1c:f2:9a": "Google", "d8:6c:63": "Google", "00:04:20": "Slim Devices",
	"18:b4:30": "Nest", "64:16:66": "Nest", "00:0e:58": "Sonos",
	"5c:aa:fd": "Sonos", "94:9f:3e": "Sonos", "b8:e9:37": "Sonos",
	"ec:fa:bc": "Espressif", "24:0a:c4": "Espressif", "84:f3:eb": "Espressif",
	"a4:cf:12": "Espressif", "cc:50:e3": "Espressif", "dc:4f:22": "Espressif",
	"b4:e6:2d": "Espressif", "68:c6:3a": "Espressif", "8c:aa:b5": "Espressif",
	"00:17:88": "Philips Hue", "ec:b5:fa": "Philips Hue",
	"00:1a:22": "eQ-3", "00:12:4b": "Texas Instruments",

	// Common NIC vendors, which at least narrow it to a PC or server
	"00:1a:a0": "Dell", "00:14:22": "Dell", "b8:2a:72": "Dell",
	"18:66:da": "Dell", "f8:bc:12": "Dell", "00:21:9b": "Dell",
	"00:25:64": "Dell", "d4:ae:52": "Dell", "84:2b:2b": "Dell",
	"00:1e:c9": "Dell", "00:26:b9": "Dell", "14:fe:b5": "Dell",
	"00:0a:f7": "Broadcom", "00:10:18": "Broadcom", "00:05:1a": "3Com",
	"00:1b:21": "Intel", "00:1e:64": "Intel", "68:05:ca": "Intel",
	"a0:36:9f": "Intel", "00:15:17": "Intel", "3c:fd:fe": "Intel",
	"90:e2:ba": "Intel", "00:1f:16": "Wistron", "00:23:24": "Quanta",
	"00:e0:81": "Tyan", "00:25:90": "Supermicro", "ac:1f:6b": "Supermicro",
	"00:30:48": "Supermicro", "3c:ec:ef": "Supermicro", "0c:c4:7a": "Supermicro",
}

// macVendor names whoever was assigned this MAC's prefix.
//
// A locally administered address — the second-least-significant bit of the
// first octet — belongs to nobody: it was made up by a hypervisor, a container
// runtime or a phone randomising its address, and reporting a vendor for it
// would be inventing one.
func macVendor(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if len(mac) < 8 {
		return ""
	}
	if b, ok := firstOctet(mac); ok && b&0x02 != 0 {
		return "randomised"
	}
	return ouiVendors[mac[:8]]
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
