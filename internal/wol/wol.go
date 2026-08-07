// Package wol wakes machines by Wake-on-LAN magic packet.
package wol

import (
	"fmt"
	"net"
)

// MagicPacket builds the standard frame: six 0xFF bytes then the target MAC
// sixteen times. Accepts any format net.ParseMAC does (colons, dashes, dots).
func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return nil, err
	}
	if len(hw) != 6 {
		return nil, fmt.Errorf("%q is not a 6-byte MAC", mac)
	}
	pkt := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		pkt = append(pkt, 0xFF)
	}
	for i := 0; i < 16; i++ {
		pkt = append(pkt, hw...)
	}
	return pkt, nil
}

// Send broadcasts magic packets for every given MAC.
//
// Packets go to the limited broadcast (255.255.255.255) and to the directed
// broadcast of every local IPv4 network, on UDP 9. The directed broadcasts are
// computed from real interface masks rather than assuming /24, and they are
// what matters on a multi-homed machine — the limited broadcast leaves by the
// default route only.
//
// Sending is best-effort by nature: the target is off and cannot acknowledge
// anything. An error here means the packets could not even be sent.
func Send(macs []string) error {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return err
	}
	defer conn.Close()

	dests := broadcastAddrs()
	dests = append(dests, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})

	var lastErr error
	sent := 0
	for _, mac := range macs {
		pkt, err := MagicPacket(mac)
		if err != nil {
			lastErr = err
			continue
		}
		for _, d := range dests {
			if _, err := conn.WriteTo(pkt, d); err != nil {
				lastErr = err
				continue
			}
			sent++
		}
	}
	if sent == 0 && lastErr != nil {
		return lastErr
	}
	return nil
}

// broadcastAddrs returns the directed broadcast address of every local IPv4
// network.
func broadcastAddrs() []net.Addr {
	var out []net.Addr
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil {
				continue
			}
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip4[i] | ^ipn.Mask[i]
			}
			out = append(out, &net.UDPAddr{IP: bcast, Port: 9})
		}
	}
	return out
}
