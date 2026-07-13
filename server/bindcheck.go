package server

import (
	"fmt"
	"net"
)

// cgnat is the 100.64.0.0/10 carrier-grade-NAT range (also used by Tailscale).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPrivateIP reports whether ip is not internet-routable (loopback, RFC1918 /
// ULA private, link-local, CGNAT, or otherwise non-global).
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || cgnat.Contains(ip) || !ip.IsGlobalUnicast()
}

// CheckBindSafety guards against exposing the hub on the public internet, which
// would be catastrophic — its tokens grant root-equivalent control of every
// host. It refuses an explicit public-IP bind unless allowPublic is set, and
// warns (via warn) when listening on all interfaces of a host that has a public
// IP. Private/loopback binds and hostnames are allowed silently.
func CheckBindSafety(addr string, allowPublic bool, warn func(string)) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" { // all interfaces
		if pub := firstPublicIP(); pub != "" && !allowPublic {
			warn(fmt.Sprintf("listening on ALL interfaces and this host has a public IP (%s) — "+
				"make sure port is firewalled. autormm must never be reachable from the internet; "+
				"bind to your LAN IP or use a zero-trust overlay (Twingate/Tailscale).", pub))
		}
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || isPrivateIP(ip) {
		return nil // a hostname we can't classify, or a private/loopback address
	}
	if allowPublic {
		warn(fmt.Sprintf("binding to PUBLIC IP %s — the hub is exposed to the internet "+
			"(allowed via -allow-public-bind). This is dangerous.", host))
		return nil
	}
	return fmt.Errorf("refusing to bind to public IP %s: autormm grants root-equivalent control of every "+
		"host and must not be exposed to the internet.\n"+
		"  Bind to a private/LAN address, or reach it remotely through a zero-trust overlay (Twingate/Tailscale).\n"+
		"  To override (strongly discouraged), pass -allow-public-bind or set AUTORMM_ALLOW_PUBLIC_BIND=1.", host)
}

// firstPublicIP returns the first internet-routable IP on any local interface.
func firstPublicIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !isPrivateIP(ipn.IP) {
			return ipn.IP.String()
		}
	}
	return ""
}
