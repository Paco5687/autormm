package server

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Naming the things a sweep finds.
//
// Reverse DNS is the obvious answer and mostly fails on a homelab: it only
// works if something authoritative holds PTR records for the LAN, which is a
// router configured to register its DHCP leases and a hub pointed at that
// router for resolution. Absent that, a discovery list is a page of addresses.
//
// So three sources are tried together and the first useful answer wins:
//
//   - reverse DNS, which is right when it is available
//   - mDNS, which is how Apple devices, printers, Chromecasts and anything
//     running Avahi answer to "who are you"
//   - NetBIOS, which is how Windows and Samba do
//
// And failing all of them, the MAC's vendor prefix, because "Ubiquiti" beside
// an address is not a name but is far more than nothing.

// nameOf identifies a device as well as it can, returning a name and the vendor
// its MAC belongs to. Either may be empty.
func nameOf(ctx context.Context, ip, mac string) (name, vendor string) {
	vendor = macVendor(mac)

	// Concurrently: each is a network round trip against a device that may not
	// answer at all, and doing them in series means three timeouts.
	var wg sync.WaitGroup
	var ptr, mdns, nbt string
	wg.Add(3)
	go func() { defer wg.Done(); ptr = reverseName(ctx, ip) }()
	go func() { defer wg.Done(); mdns = mdnsName(ctx, ip) }()
	go func() { defer wg.Done(); nbt = netbiosName(ctx, ip) }()
	wg.Wait()

	// In order of how much the answer is worth: a PTR record was configured by
	// someone, mDNS is the device's own chosen name, NetBIOS is a truncated
	// uppercase version of it.
	for _, n := range []string{ptr, mdns, nbt} {
		if n != "" {
			return n, vendor
		}
	}
	return "", vendor
}

// mdnsName asks the local network what this address calls itself.
//
// A reverse PTR query for the address, sent to the mDNS group rather than a
// resolver. Devices answer for themselves, which is why this works where a
// central DNS server knows nothing.
func mdnsName(ctx context.Context, ip string) string {
	v4 := net.ParseIP(ip).To4()
	if v4 == nil {
		return ""
	}
	qname := arpaName(v4)

	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: false},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(qname),
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return ""
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return ""
	}
	defer conn.Close()

	deadline := time.Now().Add(700 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	if _, err := conn.WriteTo(packed, group); err != nil {
		return ""
	}

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return "" // deadline: nobody claimed it
		}
		if name := parsePTRAnswer(buf[:n], qname); name != "" {
			return name
		}
		// Other traffic on the mDNS group is constant; keep reading until the
		// deadline rather than giving up on the first unrelated packet.
	}
}

// arpaName is the reverse-lookup name for an address: 10.0.0.4 becomes
// 4.0.0.10.in-addr.arpa.
func arpaName(v4 net.IP) string {
	return itoaByte(v4[3]) + "." + itoaByte(v4[2]) + "." +
		itoaByte(v4[1]) + "." + itoaByte(v4[0]) + ".in-addr.arpa."
}

func itoaByte(b byte) string {
	if b == 0 {
		return "0"
	}
	var out [3]byte
	i := len(out)
	for b > 0 {
		i--
		out[i] = byte('0' + b%10)
		b /= 10
	}
	return string(out[i:])
}

// parsePTRAnswer pulls the name out of an mDNS response for our question.
func parsePTRAnswer(packet []byte, qname string) string {
	var p dnsmessage.Parser
	if _, err := p.Start(packet); err != nil {
		return ""
	}
	if err := p.SkipAllQuestions(); err != nil {
		return ""
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return ""
		}
		if h.Type != dnsmessage.TypePTR || !strings.EqualFold(h.Name.String(), qname) {
			if err := p.SkipAnswer(); err != nil {
				return ""
			}
			continue
		}
		r, err := p.PTRResource()
		if err != nil {
			return ""
		}
		return tidyHostname(r.PTR.String())
	}
}

// netbiosName asks a Windows or Samba host for its name table.
//
// An NBSTAT query: a fixed 50-byte packet whose reply lists the names the host
// answers to. The first entry of type "workstation" is the machine name.
func netbiosName(ctx context.Context, ip string) string {
	// A node status request for the wildcard name "*", encoded the way NetBIOS
	// requires: each nibble as a letter from 'A'.
	query := []byte{
		0x00, 0x00, // transaction id
		0x00, 0x00, // flags
		0x00, 0x01, // one question
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x20,       // name length
		0x43, 0x4b, // "CK" — the encoded '*'
	}
	for i := 0; i < 15; i++ {
		query = append(query, 0x41, 0x41) // "AA" — the encoded NUL padding
	}
	query = append(query, 0x00, 0x00, 0x21, 0x00, 0x01) // NBSTAT, class IN

	conn, err := net.Dial("udp4", net.JoinHostPort(ip, "137"))
	if err != nil {
		return ""
	}
	defer conn.Close()

	deadline := time.Now().Add(600 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(query); err != nil {
		return ""
	}
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return ""
	}
	return parseNetbiosNames(buf[:n])
}

// parseNetbiosNames reads the name table out of an NBSTAT reply.
func parseNetbiosNames(b []byte) string {
	// Header (12) + the echoed 34-byte question + type/class/ttl/rdlength (10),
	// then a count byte followed by 18-byte entries.
	const offset = 12 + 34 + 10
	if len(b) <= offset {
		return ""
	}
	count := int(b[offset])
	p := offset + 1
	for i := 0; i < count; i++ {
		if p+18 > len(b) {
			return ""
		}
		name := strings.TrimSpace(string(b[p : p+15]))
		suffix := b[p+15]
		flags := b[p+16]
		p += 18
		// Suffix 0x00 is the workstation name; the group bit distinguishes a
		// workgroup entry, which is not what this machine is called.
		if suffix == 0x00 && flags&0x80 == 0 && name != "" {
			return tidyHostname(name)
		}
	}
	return ""
}

// tidyHostname trims the trailing dot and the .local/.lan suffixes that add
// nothing in a list of local devices, and rejects the names that are not names.
//
// "_gateway" is the one that turns up in practice: systemd-resolved synthesises
// it for the default route, so a resolver that knows nothing about the LAN still
// answers the router's reverse lookup with it. Showing it would be reporting a
// name that the device does not have and nobody chose.
func tidyHostname(n string) string {
	n = strings.TrimSuffix(strings.TrimSpace(n), ".")
	for _, suf := range []string{".local", ".lan", ".home", ".home.arpa"} {
		if strings.HasSuffix(strings.ToLower(n), suf) {
			n = n[:len(n)-len(suf)]
			break
		}
	}
	switch strings.ToLower(n) {
	case "_gateway", "gateway", "localhost", "localhost.localdomain", "unknown", "-":
		return ""
	}
	// A "name" that is just the address again says nothing.
	if net.ParseIP(n) != nil {
		return ""
	}
	return n
}
