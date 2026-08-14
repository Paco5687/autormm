package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// Top talkers: which ports are carrying the traffic, right now.
//
// The 80/20 of flow analysis, from data the hub already reads. It cannot say
// what the traffic is — that needs flow export and classification — but "which
// link is busy and what is on the end of it" answers most of the question most
// of the time, and it is one fetch.

// Talker is one ranked entry.
type Talker struct {
	Device string  `json:"device"`
	Port   int     `json:"port,omitempty"`
	Name   string  `json:"name,omitempty"` // the port's name, as the operator set it
	Rate   float64 `json:"rate"`           // bytes per second, both directions
	// Uplink marks a port that carries a link to another switch or the
	// gateway: its rate is everything beyond it, not one machine.
	Uplink bool       `json:"uplink,omitempty"`
	Peers  []PortPeer `json:"peers,omitempty"`
	// Whole marks an SNMP device's total, where per-port is not available.
	Whole bool `json:"whole,omitempty"`
}

func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.topTalkers(ctx))
}

func (s *Server) topTalkers(ctx context.Context) map[string]any {
	var talkers []Talker

	// Every controller-managed switch, from the one fetch that describes them
	// all — the same document the map and the port views read.
	var src NetCheck
	for _, st := range s.netChecks.list() {
		if st.NetCheck.JSONURL != "" && st.NetCheck.MAC != "" {
			src = st.NetCheck
			break
		}
	}
	if src.ID != "" {
		if doc, err := fetchStatus(ctx, src.ID, src.JSONURL, src.JSONAuth, s.netChecks.sessions); err == nil {
			talkers = append(talkers, controllerTalkers(doc)...)
		}
	}

	// Devices polled over SNMP report a whole-device rate; per-port would need
	// per-interface history the hub does not keep. Said as what it is.
	for _, st := range s.netChecks.list() {
		if st.SNMP == nil || st.SNMP.RxRate+st.SNMP.TxRate <= 0 || st.NetCheck.JSONURL != "" {
			continue
		}
		talkers = append(talkers, Talker{
			Device: st.NetCheck.Name,
			Rate:   float64(st.SNMP.RxRate + st.SNMP.TxRate),
			Whole:  true,
		})
	}

	sort.Slice(talkers, func(i, j int) bool { return talkers[i].Rate > talkers[j].Rate })
	if len(talkers) > 40 {
		talkers = talkers[:40]
	}
	// Name what is on each port, the way the port map does.
	pm := PortMap{}
	for i := range talkers {
		pm.Ports = append(pm.Ports, PortEntry{Peers: talkers[i].Peers})
	}
	s.resolvePeers(&pm)
	for i := range talkers {
		talkers[i].Peers = pm.Ports[i].Peers
	}
	return map[string]any{"talkers": talkers}
}

// controllerTalkers reads every port of every device in a controller's answer.
func controllerTalkers(doc any) []Talker {
	list, ok := jsonPath(doc, "data")
	if !ok {
		return nil
	}
	arr, ok := list.([]any)
	if !ok {
		return nil
	}
	// Ports that carry inter-switch links, marked so a busy uplink is not
	// mistaken for a busy machine: its traffic is everything beyond it.
	uplinks := map[string]bool{}
	macs := map[string]bool{}
	for _, raw := range arr {
		if d, ok := raw.(map[string]any); ok {
			macs[lowerMAC(strOf(d["mac"]))] = true
		}
	}
	for _, raw := range arr {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mac := lowerMAC(strOf(d["mac"]))
		if up, ok := d["uplink"].(map[string]any); ok {
			if peer := lowerMAC(strOf(up["uplink_mac"])); peer != "" {
				// The controller sends port numbers as numbers; reading them as
				// strings yields "", and a key ending in "#" matches every
				// port that also rendered as "" — which was all of them.
				uplinks[peer+"#"+strconv.Itoa(int(numOf(up["uplink_remote_port"])))] = true
				uplinks[mac+"#"+strconv.Itoa(int(numOf(up["port_idx"])))] = true
			}
		}
	}

	var out []Talker
	for _, raw := range arr {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		devName := firstNonEmpty(strOf(d["name"]), strOf(d["model"]))
		mac := lowerMAC(strOf(d["mac"]))
		pt, ok := d["port_table"].([]any)
		if !ok {
			continue
		}
		for _, e := range pt {
			p, ok := e.(map[string]any)
			if !ok {
				continue
			}
			rate := numOf(p["bytes-r"])
			if rate <= 0 {
				continue
			}
			tk := Talker{
				Device: devName,
				Port:   int(numOf(p["port_idx"])),
				Name:   strOf(p["name"]),
				Rate:   rate,
			}
			tk.Uplink = uplinks[mac+"#"+strconv.Itoa(tk.Port)]
			if lc, ok := p["last_connection"].(map[string]any); ok {
				if pm := lowerMAC(strOf(lc["mac"])); pm != "" {
					if macs[pm] {
						tk.Uplink = true // the far end is another managed device
					}
					tk.Peers = []PortPeer{{MAC: pm}}
				}
			}
			out = append(out, tk)
		}
	}
	return out
}
