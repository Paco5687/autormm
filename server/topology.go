package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What is connected to what.
//
// Built from what the network says about itself rather than drawn by hand,
// because a diagram anybody has to maintain is wrong within a month — which is
// the whole reason to take it from the devices.
//
// Two kinds of evidence, and they are not equal. A neighbour relation reported
// by both ends (LLDP, or a controller's own uplink record) is a fact about a
// cable. A MAC seen on a port is an inference: it says traffic from that
// address arrived here, which is usually a cable and is sometimes three
// switches away. They are kept apart all the way to the drawing, because a
// guess presented as a fact is worse than no line at all.

// TopoNode is one device on the map.
type TopoNode struct {
	MAC   string `json:"mac"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Kind  string `json:"kind"` // gateway | switch | ap | pdu | device
	IP    string `json:"ip,omitempty"`
	Up    bool   `json:"up"`
	// Leaves is how many addresses were seen on this device's ports that are
	// not themselves on the map. The map stays legible by counting them rather
	// than drawing thirty boxes; the port view lists them properly.
	Leaves int `json:"leaves,omitempty"`
	// CheckID links a node to the device being monitored, when it is one.
	CheckID string `json:"check_id,omitempty"`
}

// TopoEdge is one link.
type TopoEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	FromPort int    `json:"from_port,omitempty"`
	ToPort   int    `json:"to_port,omitempty"`
	Speed    int    `json:"speed,omitempty"`
	// Source says how this link is known: "uplink" and "lldp" are reported by
	// the devices themselves, "seen" is inferred from a MAC on a port.
	Source string `json:"source"`
}

// Topology is the whole map.
type Topology struct {
	Nodes []TopoNode `json:"nodes"`
	Edges []TopoEdge `json:"edges"`
	Root  string     `json:"root,omitempty"`
	Error string     `json:"error,omitempty"`
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.topology(ctx))
}

// topology reads the map from whichever controller the hub is configured to
// talk to. One fetch describes every device that controller manages, so any one
// device configured against it is enough to draw the whole thing.
func (s *Server) topology(ctx context.Context) Topology {
	var src NetCheck
	for _, st := range s.netChecks.list() {
		c := st.NetCheck
		if c.JSONURL != "" && c.MAC != "" {
			src = c
			break
		}
	}
	if src.ID == "" {
		return Topology{Error: "no device is configured with a controller URL, which is where the map is read from"}
	}
	doc, err := fetchStatus(ctx, src.ID, src.JSONURL, src.JSONAuth, s.netChecks.sessions)
	if err != nil {
		return Topology{Error: err.Error()}
	}
	t, unclaimed := unifiTopology(doc)
	if len(t.Nodes) == 0 && t.Error == "" {
		t.Error = "the controller listed no devices"
	}
	s.attachKnown(&t, unclaimed)
	// Wireless clients live in a second document, the station list. Best
	// effort: a map without them is still a map, and an older controller that
	// answers strangely here should not take the whole drawing down.
	if staURL := stationURL(src.JSONURL); staURL != "" {
		if staDoc, err := fetchStatus(ctx, src.ID, staURL, src.JSONAuth, s.netChecks.sessions); err == nil {
			s.attachWireless(&t, unifiStations(staDoc))
		}
	}
	s.linkToChecks(&t)
	return t
}

// stationURL derives the client list's address from the device list's.
//
// The two endpoints differ only in their last path element, which is the one
// piece of controller-API shape this relies on — and it holds for both the
// self-hosted and the UniFi OS forms, because the prefix is carried over
// verbatim from a URL that is already known to work.
func stationURL(deviceURL string) string {
	const dev, sta = "/stat/device", "/stat/sta"
	if !strings.HasSuffix(deviceURL, dev) {
		return ""
	}
	return strings.TrimSuffix(deviceURL, dev) + sta
}

// station is one associated client, as the controller reports it.
type station struct {
	mac   string
	apMAC string
	name  string
	ip    string
}

// unifiStations reads the wireless clients out of a station list. Wired
// clients appear in the same list; they are already covered by the port
// tables, and better — a port number beats "somewhere on this switch".
func unifiStations(doc any) []station {
	list, ok := jsonPath(doc, "data")
	if !ok {
		return nil
	}
	arr, ok := list.([]any)
	if !ok {
		return nil
	}
	var out []station
	for _, raw := range arr {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if wired, ok := m["is_wired"].(bool); !ok || wired {
			continue
		}
		st := station{
			mac:   lowerMAC(strOf(m["mac"])),
			apMAC: lowerMAC(strOf(m["ap_mac"])),
			// The operator's alias wins over what the device calls itself.
			name: firstNonEmpty(strOf(m["name"]), strOf(m["hostname"])),
			ip:   strOf(m["ip"]),
		}
		if st.mac != "" && st.apMAC != "" {
			out = append(out, st)
		}
	}
	return out
}

// attachWireless hangs wireless clients off their access points.
//
// The same bargain as the wired side: a client the hub knows — a monitored
// device, a host running an agent — is drawn and named, and the rest are a
// count on the AP. Nineteen phones as nineteen boxes is a map nobody asked
// for; the one wireless thing that is also a card on this dashboard is
// exactly what was missing.
func (s *Server) attachWireless(t *Topology, stations []station) {
	onMap := map[string]int{}
	for i, n := range t.Nodes {
		onMap[n.MAC] = i
	}
	known := s.knownByMAC()
	for _, st := range stations {
		ap, ok := onMap[st.apMAC]
		if !ok {
			continue // an AP the map does not have cannot anchor anything
		}
		if _, dup := onMap[st.mac]; dup {
			continue // already on the map — wired, or roamed and counted once
		}
		k, isKnown := known[st.mac]
		if !isKnown {
			t.Nodes[ap].Leaves++
			continue
		}
		onMap[st.mac] = len(t.Nodes)
		t.Nodes = append(t.Nodes, TopoNode{
			MAC: st.mac, Name: firstNonEmpty(k.name, st.name), Kind: k.kind,
			Up: k.up, IP: st.ip, CheckID: k.checkID,
		})
		t.Edges = append(t.Edges, TopoEdge{From: st.apMAC, To: st.mac, Source: "wifi"})
	}
}

// knownMAC is what the hub can say about an address it recognises.
type knownMAC struct {
	name    string
	kind    string
	up      bool
	checkID string
}

// knownByMAC indexes everything on the dashboard that has an address: the
// monitored devices by their configured MAC, and the hosts by the interfaces
// their agents report.
func (s *Server) knownByMAC() map[string]knownMAC {
	byMAC := map[string]knownMAC{}
	for _, st := range s.netChecks.list() {
		if st.NetCheck.MAC != "" {
			byMAC[lowerMAC(st.NetCheck.MAC)] = knownMAC{
				name: st.NetCheck.Name, kind: "device", up: st.Up, checkID: st.NetCheck.ID,
			}
		}
	}
	// Hosts after devices: if a machine is somehow both, the agent's name is
	// the one people use.
	for _, v := range s.store.views() {
		for _, mac := range v.Facts.MACs {
			if m := lowerMAC(mac); m != "" {
				byMAC[m] = knownMAC{name: v.Hostname, kind: "host", up: v.Online}
			}
		}
	}
	return byMAC
}

// attachKnown puts the rest of the dashboard on the map.
//
// The controller only describes its own gear, but the switches see everybody:
// every UPS, printer and server leaves its address on the port it hangs off.
// The hub knows most of those addresses already — monitored devices carry a
// MAC, and every agent reports its interfaces — so what the extraction counted
// as an anonymous leaf is very often a card on this same dashboard. Naming it
// and drawing the line is the difference between "+3" and seeing the UPS on
// port 21.
func (s *Server) attachKnown(t *Topology, unclaimed []TopoEdge) {
	onMap := map[string]int{}
	for i, n := range t.Nodes {
		onMap[n.MAC] = i
	}

	// A port that carries a link between switches sees the addresses of
	// everything beyond it — that is what a bridge does. An address seen there
	// is transit, not attachment, so those ports claim nothing.
	transit := map[string]bool{}
	for _, e := range t.Edges {
		if e.FromPort > 0 {
			transit[e.From+"#"+strconv.Itoa(e.FromPort)] = true
		}
		if e.ToPort > 0 {
			transit[e.To+"#"+strconv.Itoa(e.ToPort)] = true
		}
	}

	byMAC := s.knownByMAC()

	for _, e := range unclaimed {
		if transit[e.From+"#"+strconv.Itoa(e.FromPort)] {
			continue
		}
		k, ok := byMAC[e.To]
		if !ok {
			continue
		}
		if _, dup := onMap[e.To]; dup {
			continue // already attached via another port; one node is plenty
		}
		onMap[e.To] = len(t.Nodes)
		t.Nodes = append(t.Nodes, TopoNode{
			MAC: e.To, Name: k.name, Kind: k.kind, Up: k.up, CheckID: k.checkID,
		})
		e.Source = "seen"
		t.Edges = append(t.Edges, e)
		// It was counted as an anonymous leaf; it is not one any more.
		if i, ok := onMap[e.From]; ok && t.Nodes[i].Leaves > 0 {
			t.Nodes[i].Leaves--
		}
	}
}

// unifiTopology builds the map from a controller's answer.
func unifiTopology(doc any) (Topology, []TopoEdge) {
	list, ok := jsonPath(doc, "data")
	if !ok {
		return Topology{Error: "the answer has no device list"}, nil
	}
	arr, ok := list.([]any)
	if !ok {
		return Topology{Error: "the device list is not a list"}, nil
	}

	nodes := map[string]*TopoNode{}
	var edges []TopoEdge
	// Held back until every device is known: whether an address on a port is a
	// link or a leaf depends on whether it turns out to be on the map, and the
	// device it names may not have been read yet.
	var seen []TopoEdge
	// gateways names the address every device says it reaches the internet
	// through. The gateway itself is often not in the list — it is managed
	// separately — so it has to be inferred from everything pointing at it.
	gateways := map[string]int{}

	for _, raw := range arr {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mac := lowerMAC(strOf(d["mac"]))
		if mac == "" {
			continue
		}
		n := &TopoNode{
			MAC:   mac,
			Name:  firstNonEmpty(strOf(d["name"]), strOf(d["model"]), mac),
			Model: strOf(d["model"]),
			Kind:  unifiKind(strOf(d["type"]), d),
			IP:    strOf(d["ip"]),
			Up:    numOf(d["state"]) == 1,
		}
		nodes[mac] = n
		if g := lowerMAC(strOf(d["gateway_mac"])); g != "" {
			gateways[g]++
		}

		// The device's own record of what it hangs off. Both port numbers and
		// the far end's name come with it, which no other source gives.
		if up, ok := d["uplink"].(map[string]any); ok {
			if peer := lowerMAC(strOf(up["uplink_mac"])); peer != "" && peer != mac {
				edges = append(edges, TopoEdge{
					From: peer, To: mac,
					FromPort: int(numOf(up["uplink_remote_port"])),
					ToPort:   int(numOf(up["port_idx"])),
					Speed:    int(numOf(up["speed"])),
					Source:   "uplink",
				})
				if name := strOf(up["uplink_device_name"]); name != "" {
					if _, known := nodes[peer]; !known {
						nodes[peer] = &TopoNode{MAC: peer, Name: name, Kind: "switch", Up: true}
					}
				}
			}
		}

		// LLDP neighbours: the same class of fact, and the only source for a
		// link whose far end does not report an uplink.
		if lldp, ok := d["lldp_table"].([]any); ok {
			for _, e := range lldp {
				l, ok := e.(map[string]any)
				if !ok {
					continue
				}
				peer := lowerMAC(strOf(l["chassis_id"]))
				if peer == "" || peer == mac {
					continue
				}
				edges = append(edges, TopoEdge{
					From: mac, To: peer,
					FromPort: int(numOf(l["local_port_idx"])),
					Source:   "lldp",
				})
			}
		}

		// What has been seen on each port.
		//
		// Two different things depending on what it turns out to be. Another
		// device on the map is a link — weaker evidence than a reported
		// neighbour, but the only evidence there is when one end's uplink
		// record predates the field naming the far end. Anything else is a leaf
		// and gets counted: thirty boxes hanging off one switch is a picture
		// nobody can read, and the port view lists them by name.
		if pt, ok := d["port_table"].([]any); ok {
			for _, e := range pt {
				p, ok := e.(map[string]any)
				if !ok {
					continue
				}
				lc, ok := p["last_connection"].(map[string]any)
				if !ok {
					continue
				}
				peer := lowerMAC(strOf(lc["mac"]))
				if peer == "" || peer == mac {
					continue
				}
				seen = append(seen, TopoEdge{
					From: mac, To: peer,
					FromPort: int(numOf(p["port_idx"])),
					Speed:    int(numOf(p["speed"])),
					Source:   "seen",
				})
			}
		}
	}

	// Now that every device is known, sort out what each seen address means.
	//
	// A port that carries a link between switches sees the address of
	// everything beyond it — that is what a bridge does — so an address on such
	// a port is transit, and says nothing about where the thing is attached. It
	// is not even a leaf: the device it belongs to is counted wherever it
	// really hangs. Likewise a device whose attachment is already reported by
	// an uplink or LLDP needs no second, weaker line from somewhere else that
	// merely saw its traffic go by.
	transit := map[string]bool{}
	attached := map[string]bool{}
	for _, e := range edges {
		if e.FromPort > 0 {
			transit[e.From+"#"+strconv.Itoa(e.FromPort)] = true
		}
		if e.ToPort > 0 {
			transit[e.To+"#"+strconv.Itoa(e.ToPort)] = true
		}
		attached[e.From], attached[e.To] = true, true
	}
	var unclaimed []TopoEdge
	for _, e := range seen {
		if transit[e.From+"#"+strconv.Itoa(e.FromPort)] {
			continue
		}
		if attached[e.To] {
			continue
		}
		_, isNode := nodes[e.To]
		if isNode || gateways[e.To] > 0 {
			// A gateway is a link even though the controller does not manage
			// it: it is how the rack reaches the world, and drawing the rack
			// with nothing above it is a worse error than a dashed line.
			edges = append(edges, e)
			attached[e.To] = true
			continue
		}
		if n := nodes[e.From]; n != nil {
			n.Leaves++
		}
		unclaimed = append(unclaimed, e)
	}

	// Anything a link points at that the controller does not manage.
	//
	// A router is the usual case and it matters: the switch below it reports the
	// link, so leaving the far end off the map deletes the link too, and the
	// whole rack ends up with nothing above it. These are marked so that ones
	// nothing turned out to reference can be dropped again.
	synthetic := map[string]bool{}
	for _, e := range edges {
		for _, mac := range []string{e.From, e.To} {
			if _, known := nodes[mac]; known {
				continue
			}
			nodes[mac] = &TopoNode{
				MAC:  mac,
				Name: firstNonEmpty(macVendor(mac), mac),
				Kind: "device",
				Up:   true,
			}
			synthetic[mac] = true
		}
	}
	// A gateway everything names as its way out. It is often not in the list,
	// because the controller does not manage it — and left out, the map roots
	// at whatever switch sits highest and implies the rack has no way out.
	root := ""
	for g, n := range gateways {
		if _, known := nodes[g]; !known && n >= 2 {
			nodes[g] = &TopoNode{MAC: g, Name: "gateway", Kind: "gateway", Up: true}
			synthetic[g] = true
		}
		if _, known := nodes[g]; known {
			nodes[g].Kind = "gateway"
			// It may already have been invented by a link pointing at it, in
			// which case it is named after whoever made the hardware. Being the
			// way out is the more useful thing to say about it.
			if synthetic[g] {
				nodes[g].Name = "gateway"
			}
			root = g
		}
	}

	// One box has as many addresses as it has interfaces, and a router shows
	// two of them here: the one its neighbour sees over LLDP and the one every
	// device names as its way out. Drawn separately that is two routers, each
	// with a real line — so an invented node whose address sits in the same
	// block as the gateway's is folded into it. Only invented nodes: a managed
	// device with a coincidentally similar address is a real, separate thing.
	if root != "" {
		block := root[:len(root)-2]
		for mac, n := range nodes {
			if mac == root || !synthetic[mac] || n.Kind == "gateway" {
				continue
			}
			if len(mac) == len(root) && strings.HasPrefix(mac, block) {
				delete(nodes, mac)
				for i := range edges {
					if edges[i].From == mac {
						edges[i].From = root
					}
					if edges[i].To == mac {
						edges[i].To = root
					}
				}
			}
		}
	}

	out := Topology{Root: root}
	out.Edges = dedupeEdges(edges, nodes)

	// A router can appear under two addresses — the one its neighbours see over
	// LLDP and the one every device names as its way out are different
	// interfaces of the same box. Only the address something actually links to
	// is evidence of anything, so an invented node nothing references is
	// dropped rather than left floating as a second router.
	deg := map[string]int{}
	for _, e := range out.Edges {
		deg[e.From]++
		deg[e.To]++
	}
	for mac := range nodes {
		if synthetic[mac] && deg[mac] == 0 {
			delete(nodes, mac)
		}
	}
	if _, ok := nodes[root]; !ok {
		root = ""
	}

	for _, n := range nodes {
		out.Nodes = append(out.Nodes, *n)
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })
	out.Root = root
	if out.Root == "" {
		out.Root = rootOf(out.Nodes, out.Edges)
	}
	return out, unclaimed
}

// dedupeEdges keeps one line per cable.
//
// The same link is reported from both ends and by two mechanisms, so it arrives
// up to four times. The copy with the most detail wins: an uplink record names
// both ports and the speed, an LLDP entry names one port, and neither is worth
// drawing twice.
func dedupeEdges(edges []TopoEdge, nodes map[string]*TopoNode) []TopoEdge {
	best := map[string]TopoEdge{}
	order := []string{}
	for _, e := range edges {
		if _, ok := nodes[e.From]; !ok {
			continue // a neighbour that is not on the map is not a line
		}
		if _, ok := nodes[e.To]; !ok {
			continue
		}
		a, b := e.From, e.To
		if a > b {
			a, b = b, a
		}
		k := a + "|" + b
		cur, seen := best[k]
		if !seen {
			order = append(order, k)
			best[k] = e
			continue
		}
		if edgeDetail(e) > edgeDetail(cur) {
			best[k] = e
		}
	}
	out := make([]TopoEdge, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

func edgeDetail(e TopoEdge) int {
	n := 0
	if e.Source == "uplink" {
		n += 4 // both ends, both ports, and a speed
	}
	if e.FromPort > 0 {
		n++
	}
	if e.ToPort > 0 {
		n++
	}
	return n
}

// rootOf picks where to hang the map when no gateway was found: the device with
// the most links, which in a rack is the core switch.
func rootOf(nodes []TopoNode, edges []TopoEdge) string {
	deg := map[string]int{}
	for _, e := range edges {
		deg[e.From]++
		deg[e.To]++
	}
	best, bestN := "", -1
	for _, n := range nodes {
		if deg[n.MAC] > bestN {
			best, bestN = n.MAC, deg[n.MAC]
		}
	}
	return best
}

// linkToChecks ties a node to the device being monitored, so the map can lead
// somewhere rather than only describing.
func (s *Server) linkToChecks(t *Topology) {
	byMAC := map[string]NetCheck{}
	for _, st := range s.netChecks.list() {
		if st.NetCheck.MAC != "" {
			byMAC[lowerMAC(st.NetCheck.MAC)] = st.NetCheck
		}
	}
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if c, ok := byMAC[n.MAC]; ok {
			n.CheckID = c.ID
			if c.Name != "" {
				n.Name = c.Name
			}
		}
		if n.IP == "" {
			if ip, ok := s.netChecks.macs.lookup(n.MAC); ok {
				n.IP = ip
			}
		}
	}
}

// unifiKind sorts a device into something the map can draw differently. A PDU
// is a switch as far as the controller is concerned — it reports a port table
// and an uplink — but it is not one on a diagram.
func unifiKind(t string, d map[string]any) string {
	switch t {
	case "uap":
		return "ap"
	case "ugw", "udm":
		return "gateway"
	case "usw":
		if _, isPDU := d["outlet_table"].([]any); isPDU {
			return "pdu"
		}
		return "switch"
	}
	return "device"
}

func lowerMAC(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
