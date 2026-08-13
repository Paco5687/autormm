package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// What is plugged into which port.
//
// The question a rack asks constantly and nothing on the dashboard could
// answer: which port is that machine on, what is on port 14, what did I unplug.
// Walking to the rack or signing into a controller to find out is the thing
// this is here to stop.
//
// The switch already knows. A managed one keeps a table of which MAC it has
// seen on which port, and a controller that manages it keeps the same table
// with the port names its operator typed. Reading it is the whole feature.

// PortEntry is one port and what has been seen on it.
type PortEntry struct {
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"`
	Up    bool   `json:"up"`
	Speed int    `json:"speed,omitempty"` // Mbps
	Media string `json:"media,omitempty"`
	// PoE is what the port is delivering, for a switch that reports it.
	PoEWatts float64 `json:"poe_watts,omitempty"`
	PoEOn    bool    `json:"poe_on,omitempty"`
	// Peers is what has been seen on this port. More than one means an uplink
	// or a hypervisor rather than a machine.
	Peers []PortPeer `json:"peers,omitempty"`
	// Traffic is the current combined rate in bytes per second, when known.
	Traffic float64 `json:"traffic,omitempty"`
}

// PortPeer is one thing seen on a port, named as well as it can be.
type PortPeer struct {
	MAC string `json:"mac"`
	// Name is what this MAC is on the dashboard, when it is anything: a device
	// being monitored, or a host running an agent.
	Name   string `json:"name,omitempty"`
	IP     string `json:"ip,omitempty"`
	Vendor string `json:"vendor,omitempty"`
	// Stale marks an entry the switch has not seen recently. A port whose entry
	// aged out is not an empty port, and saying "nothing" would be a lie.
	Stale bool `json:"stale,omitempty"`
}

// PortMap is one switch's answer.
type PortMap struct {
	Device string      `json:"device"`
	Source string      `json:"source"` // where this came from, so a gap is attributable
	Ports  []PortEntry `json:"ports"`
	Error  string      `json:"error,omitempty"`
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.URL.Query().Get("id")
	c, ok := s.netChecks.byID(id)
	if !ok {
		http.Error(w, "no such device", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	pm := s.portsFor(ctx, c)
	pm.Device = c.Name
	s.resolvePeers(&pm)
	writeJSON(w, http.StatusOK, pm)
}

// portsFor reads a device's port table from whatever can answer for it.
func (s *Server) portsFor(ctx context.Context, c NetCheck) PortMap {
	if c.JSONURL == "" || c.MAC == "" {
		return PortMap{Error: "this device has no controller URL and MAC set, which is where its port table is read from"}
	}
	doc, err := fetchStatus(ctx, c.ID, c.JSONURL, c.JSONAuth, s.netChecks.sessions)
	if err != nil {
		return PortMap{Error: err.Error()}
	}
	ports, ok := unifiPorts(doc, c.MAC)
	if !ok {
		return PortMap{Error: "no port table for this device in the controller's answer — is the MAC right?"}
	}
	return PortMap{Source: "controller", Ports: ports}
}

// unifiPorts pulls one device's port table out of a controller's answer.
//
// The controller replies for every device it has adopted at once, so the MAC is
// what picks this one out — the same way a reading does.
func unifiPorts(doc any, mac string) ([]PortEntry, bool) {
	dev, ok := jsonPath(doc, "data[mac="+strings.ToLower(strings.TrimSpace(mac))+"]")
	if !ok {
		return nil, false
	}
	m, ok := dev.(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := m["port_table"].([]any)
	if !ok {
		return nil, false
	}
	out := make([]PortEntry, 0, len(raw))
	for _, r := range raw {
		p, ok := r.(map[string]any)
		if !ok {
			continue
		}
		e := PortEntry{
			Index:    int(numOf(p["port_idx"])),
			Name:     strOf(p["name"]),
			Up:       boolOf(p["up"]),
			Speed:    int(numOf(p["speed"])),
			Media:    strOf(p["media"]),
			PoEOn:    boolOf(p["poe_enable"]) && boolOf(p["poe_good"]),
			PoEWatts: numOf(p["poe_power"]),
			Traffic:  numOf(p["bytes-r"]),
		}
		// last_connection is the switch's own record of what it saw here. It
		// survives the device going away, which is the point: a port that has
		// gone quiet is more interesting than one that was always empty.
		if lc, ok := p["last_connection"].(map[string]any); ok {
			if mac := strOf(lc["mac"]); mac != "" {
				peer := PortPeer{MAC: strings.ToLower(mac)}
				// Anything not currently linked is a memory, not an occupant.
				peer.Stale = !e.Up
				e.Peers = append(e.Peers, peer)
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, true
}

// resolvePeers turns MAC addresses into things a person recognises.
//
// A MAC on a port is an opaque number, and the hub already holds three ways to
// name one: the devices it monitors, the addresses ARP has seen, and the
// registry that says who made the hardware. An unnamed port is then genuinely
// unknown rather than merely unlabelled — which is the interesting case.
func (s *Server) resolvePeers(pm *PortMap) {
	byMAC := map[string]NetCheck{}
	for _, st := range s.netChecks.list() {
		if st.NetCheck.MAC != "" {
			byMAC[strings.ToLower(st.NetCheck.MAC)] = st.NetCheck
		}
	}
	for i := range pm.Ports {
		for j := range pm.Ports[i].Peers {
			p := &pm.Ports[i].Peers[j]
			if c, ok := byMAC[p.MAC]; ok {
				p.Name = c.Name
			}
			if ip, ok := s.netChecks.macs.lookup(p.MAC); ok {
				p.IP = ip
			}
			p.Vendor = macVendor(p.MAC)
		}
	}
}

func numOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}
