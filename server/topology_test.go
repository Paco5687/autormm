package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// A rack shaped like a real one: a gateway that is not in the list, a core
// switch, an access switch below it, an AP on the access switch, two PDUs, and
// leaf devices on ports. Every relation appears the way the controller really
// reports it — from both ends, and by two mechanisms.
const rackDoc = `{"data":[
 {"mac":"8c:30:66:d0:6e:43","name":"USW Pro HD 24","model":"USWED73","type":"usw","state":1,
  "ip":"10.0.0.245","gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"64:62:66:23:c2:28","uplink_remote_port":3,"port_idx":24,"speed":2500},
  "lldp_table":[{"chassis_id":"d8:b3:70:83:96:77","local_port_idx":28,"port_id":"Port 25"},
                {"chassis_id":"d8:b3:70:1b:6c:2b","local_port_idx":19}],
  "port_table":[{"port_idx":1,"last_connection":{"mac":"aa:bb:cc:00:00:01"}},
                {"port_idx":2,"last_connection":{"mac":"aa:bb:cc:00:00:02"}},
                {"port_idx":28,"last_connection":{"mac":"d8:b3:70:83:96:77"}}]},
 {"mac":"d8:b3:70:83:96:77","name":"US 24 PoE 250W","model":"US24P250","type":"usw","state":1,
  "ip":"10.0.0.246","gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":28,"port_idx":25,"speed":1000,
            "uplink_device_name":"USW Pro HD 24"},
  "lldp_table":[{"chassis_id":"8c:30:66:80:95:7c","local_port_idx":2}],
  "port_table":[{"port_idx":2,"last_connection":{"mac":"8c:30:66:80:95:7c"}},
                {"port_idx":18,"last_connection":{"mac":"aa:bb:cc:00:00:03"}}]},
 {"mac":"8c:30:66:80:95:7c","name":"Access Point - Basement","model":"UAPA6A9","type":"uap","state":1,
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"d8:b3:70:83:96:77","uplink_remote_port":2,"port_idx":1,"speed":1000}},
 {"mac":"d8:b3:70:1b:6c:2b","name":"TOP USP PDU Pro","model":"USPPDUP","type":"usw","state":1,
  "gateway_mac":"64:62:66:23:c2:28","outlet_table":[{"index":1,"name":"USB 1"}],
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":19,"port_idx":1,"speed":100}},
 {"mac":"ac:8b:a9:58:f7:b2","name":"USW Lite 16 PoE","model":"USL16LP","type":"usw","state":0,
  "gateway_mac":"64:62:66:23:c2:28"}]}`

func decodeRack(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(rackDoc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTopologyFindsEveryDeviceAndTheGateway(t *testing.T) {
	top, _ := unifiTopology(decodeRack(t))
	if top.Error != "" {
		t.Fatalf("error: %s", top.Error)
	}
	byMAC := map[string]TopoNode{}
	for _, n := range top.Nodes {
		byMAC[n.MAC] = n
	}
	// Five devices in the list, plus the gateway everything points at and
	// nothing describes — leaving it out would root the map at a switch and
	// imply the rack has no way out.
	if len(top.Nodes) != 6 {
		t.Fatalf("got %d nodes: %+v", len(top.Nodes), top.Nodes)
	}
	gw, ok := byMAC["64:62:66:23:c2:28"]
	if !ok || gw.Kind != "gateway" {
		t.Errorf("gateway = %+v", gw)
	}
	if top.Root != "64:62:66:23:c2:28" {
		t.Errorf("root = %q, want the gateway", top.Root)
	}
	// A PDU reports a port table and an uplink, so the controller calls it a
	// switch. It is not one on a diagram.
	if k := byMAC["d8:b3:70:1b:6c:2b"].Kind; k != "pdu" {
		t.Errorf("PDU kind = %q", k)
	}
	if k := byMAC["8c:30:66:80:95:7c"].Kind; k != "ap" {
		t.Errorf("AP kind = %q", k)
	}
	// A device the controller has adopted but cannot reach is still on the map,
	// and saying so is the point.
	if n := byMAC["ac:8b:a9:58:f7:b2"]; n.Up {
		t.Error("a disconnected switch was drawn as up")
	}
}

// The same cable is reported from both ends and by two mechanisms, so it
// arrives up to four times. One line per cable, and the copy with the most
// detail wins.
func TestEachCableIsOneLine(t *testing.T) {
	top, _ := unifiTopology(decodeRack(t))
	if len(top.Edges) != 4 {
		t.Fatalf("got %d edges, want 4 (gateway-core, core-access, access-ap, core-pdu): %+v",
			len(top.Edges), top.Edges)
	}
	var coreAccess *TopoEdge
	for i := range top.Edges {
		e := &top.Edges[i]
		if (e.From == "8c:30:66:d0:6e:43" && e.To == "d8:b3:70:83:96:77") ||
			(e.To == "8c:30:66:d0:6e:43" && e.From == "d8:b3:70:83:96:77") {
			coreAccess = e
		}
	}
	if coreAccess == nil {
		t.Fatalf("the core-to-access link is missing: %+v", top.Edges)
	}
	// That link is reported four ways. The uplink record is the one worth
	// keeping: it is the only one carrying both port numbers and the speed.
	if coreAccess.Source != "uplink" {
		t.Errorf("kept the %s copy, which has less detail", coreAccess.Source)
	}
	if coreAccess.FromPort == 0 || coreAccess.ToPort == 0 {
		t.Errorf("both port numbers should survive: %+v", coreAccess)
	}
	if coreAccess.Speed != 1000 {
		t.Errorf("speed = %d", coreAccess.Speed)
	}
}

// Everything else on a port is counted, not drawn: thirty boxes hanging off one
// switch is a picture nobody can read, and the port view lists them properly.
func TestLeavesAreCountedNotDrawn(t *testing.T) {
	top, _ := unifiTopology(decodeRack(t))
	byMAC := map[string]TopoNode{}
	for _, n := range top.Nodes {
		byMAC[n.MAC] = n
	}
	// The core switch has three port entries, one of which is the access switch.
	// That one is a link and is drawn as a line; counting it as well would
	// report the rack as containing two devices it does not have.
	if got := byMAC["8c:30:66:d0:6e:43"].Leaves; got != 2 {
		t.Errorf("core leaves = %d, want 2 — the third is the access switch", got)
	}
	// None of those addresses became nodes.
	for _, n := range top.Nodes {
		if n.MAC == "aa:bb:cc:00:00:01" {
			t.Error("a leaf address was drawn as a device")
		}
	}
}

// A neighbour the controller does not manage is still a real neighbour.
//
// This rule used to be the other way round — an LLDP entry pointing at anything
// not in the controller's list was discarded — and that is what flattened a real
// map. The router below is exactly the case: the switch reports the link, the
// controller does not manage the router, so dropping the far end deleted the
// link and left the rack with nothing above it.
func TestNeighboursTheControllerDoesNotManageAreStillDrawn(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[{"mac":"aa:aa:aa:aa:aa:aa","name":"core","type":"usw","state":1,
	  "lldp_table":[{"chassis_id":"ff:ff:ff:ff:ff:ff","local_port_idx":1}]}]}`), &v)
	top, _ := unifiTopology(v)
	if len(top.Edges) != 1 {
		t.Fatalf("the reported link was dropped: %+v", top.Edges)
	}
	if len(top.Nodes) != 2 {
		t.Fatalf("nodes = %+v", top.Nodes)
	}
	// Named as well as it can be, which for an address and nothing else is who
	// made it.
	for _, n := range top.Nodes {
		if n.MAC == "ff:ff:ff:ff:ff:ff" && n.Name == "" {
			t.Error("the unmanaged neighbour got no name at all")
		}
	}
}

// A router has as many addresses as interfaces, and it shows two of them here:
// the one its neighbour sees over LLDP and the one every device names as its
// way out. Drawn separately that is two routers, each with a real line — which
// is what a real map showed. They are one box and must fold into one node.
func TestARoutersTwoInterfacesAreOneBox(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[
	 {"mac":"aa:aa:aa:aa:aa:aa","name":"core","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "lldp_table":[{"chassis_id":"64:62:66:23:c2:25","local_port_idx":27}]},
	 {"mac":"bb:bb:bb:bb:bb:bb","name":"access","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"uplink_mac":"aa:aa:aa:aa:aa:aa","uplink_remote_port":1,"port_idx":9}}]}`), &v)
	top, _ := unifiTopology(v)
	routers := 0
	for _, n := range top.Nodes {
		if n.MAC == "64:62:66:23:c2:25" || n.MAC == "64:62:66:23:c2:28" {
			routers++
			if n.Kind != "gateway" {
				t.Errorf("the folded router is a %q", n.Kind)
			}
		}
	}
	if routers != 1 {
		t.Fatalf("the router appears %d times: %+v", routers, top.Nodes)
	}
	// The LLDP line survives the fold, re-aimed at the surviving node.
	if len(top.Edges) != 2 {
		t.Errorf("edges = %+v", top.Edges)
	}
	if top.Root != "64:62:66:23:c2:28" {
		t.Errorf("root = %q", top.Root)
	}
	// A managed device that happens to sit in the same address block is a real,
	// separate thing and must never be folded.
	var v2 any
	json.Unmarshal([]byte(`{"data":[
	 {"mac":"64:62:66:23:c2:25","name":"a real device","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"uplink_mac":"64:62:66:23:c2:28","uplink_remote_port":1,"port_idx":9}}]}`), &v2)
	top2, _ := unifiTopology(v2)
	kept := false
	for _, n := range top2.Nodes {
		if n.MAC == "64:62:66:23:c2:25" && n.Name == "a real device" {
			kept = true
		}
	}
	if !kept {
		t.Error("a managed device was folded into the gateway")
	}
}

// The shape that flattened the map: a core switch whose uplink record predates
// the field naming the far end, so the only evidence of the router is LLDP.
// Every device below it must still land under it rather than beside it.
func TestALegacyUplinkStillProducesATree(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[
	 {"mac":"8c:30:66:d0:6e:43","name":"core","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"mac":"8c:30:66:d0:6e:43","port_idx":24,"speed":2500,"uplink_source":"legacy"},
	  "lldp_table":[{"chassis_id":"64:62:66:23:c2:25","local_port_idx":27}]},
	 {"mac":"d8:b3:70:83:96:77","name":"access","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":28,"port_idx":25,"speed":1000}},
	 {"mac":"8c:30:66:80:95:7c","name":"ap","type":"uap","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"uplink_mac":"d8:b3:70:83:96:77","uplink_remote_port":2,"port_idx":1}}]}`), &v)
	top, _ := unifiTopology(v)
	// router — core — access — ap: three links, and every device reachable from
	// the root. A break anywhere here is what puts everything on one row.
	if len(top.Edges) != 3 {
		t.Fatalf("edges = %+v", top.Edges)
	}
	adj := map[string][]string{}
	for _, e := range top.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}
	seen := map[string]bool{top.Root: true}
	queue := []string{top.Root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if !seen[nb] {
				seen[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	if len(seen) != len(top.Nodes) {
		t.Errorf("%d of %d devices are reachable from the root — the rest render as one flat row",
			len(seen), len(top.Nodes))
	}
}

// The controller only describes its own gear, but the switches see everybody.
// An address the hub already knows — a monitored device, or a host whose agent
// reports its interfaces — must become a named node on its port, not an
// anonymous "+1".
func TestDashboardDevicesAndHostsJoinTheMap(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[
	 {"mac":"aa:aa:aa:aa:aa:aa","name":"core","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "port_table":[{"port_idx":21,"speed":1000,"last_connection":{"mac":"28:29:86:6f:a1:d5"}},
	                {"port_idx":1,"speed":2500,"last_connection":{"mac":"d8:bb:c1:a5:ee:5f"}},
	                {"port_idx":9,"last_connection":{"mac":"f8:5a:00:33:e4:57"}},
	                {"port_idx":28,"last_connection":{"mac":"bb:bb:bb:bb:bb:bb"}}]},
	 {"mac":"bb:bb:bb:bb:bb:bb","name":"access","type":"usw","state":1,"gateway_mac":"64:62:66:23:c2:28",
	  "uplink":{"uplink_mac":"aa:aa:aa:aa:aa:aa","uplink_remote_port":28,"port_idx":25,"speed":1000},
	  "port_table":[{"port_idx":3,"last_connection":{"mac":"28:29:86:6f:a1:d5"}}]}]}`), &v)

	s := &Server{store: NewStore(60, time.Minute, nil), netChecks: newNetChecks(t.TempDir())}
	upsCheck := NetCheck{ID: "ups", Name: "Top UPS", MAC: "28:29:86:6f:a1:d5"}
	s.netChecks.checks["ups"] = &upsCheck
	// The state embeds the whole check, the way the poll loop stores it — the
	// listing prefers the state's copy, so a bare ID here loses the MAC.
	s.netChecks.state["ups"] = &NetStatus{NetCheck: upsCheck, Up: true}
	s.store.register(protocol.Register{AgentID: "tron", Hostname: "tron",
		Facts: protocol.HostFacts{MACs: []string{"D8:BB:C1:A5:EE:5F"}}}, nil)

	top, unclaimed := unifiTopology(v)
	s.attachKnown(&top, unclaimed)

	byMAC := map[string]TopoNode{}
	for _, n := range top.Nodes {
		byMAC[n.MAC] = n
	}
	ups, ok := byMAC["28:29:86:6f:a1:d5"]
	if !ok || ups.Name != "Top UPS" || ups.Kind != "device" || ups.CheckID != "ups" {
		t.Errorf("UPS = %+v", ups)
	}
	// The agent reported its MAC in upper case; the switch reports it lower.
	host, ok := byMAC["d8:bb:c1:a5:ee:5f"]
	if !ok || host.Name != "tron" || host.Kind != "host" {
		t.Errorf("host = %+v", host)
	}
	// The Espressif thing the hub has never heard of stays a count.
	if _, drawn := byMAC["f8:5a:00:33:e4:57"]; drawn {
		t.Error("an unknown MAC was drawn as a node")
	}
	if got := byMAC["aa:aa:aa:aa:aa:aa"].Leaves; got != 1 {
		t.Errorf("core leaves = %d, want 1 — the UPS and the host were claimed", got)
	}

	// The UPS's address was also seen on the core's port 28 — the port that
	// carries the link to the access switch. A bridge port sees everything
	// beyond it; attaching the UPS there would hang it off the wrong switch.
	// It is on the access switch's port 3 in this scenario? No — core port 21.
	var upsEdges []TopoEdge
	for _, e := range top.Edges {
		if e.To == "28:29:86:6f:a1:d5" {
			upsEdges = append(upsEdges, e)
		}
	}
	if len(upsEdges) != 1 {
		t.Fatalf("UPS has %d edges: %+v", len(upsEdges), upsEdges)
	}
	if upsEdges[0].From != "aa:aa:aa:aa:aa:aa" || upsEdges[0].FromPort != 21 {
		t.Errorf("UPS attached at %s port %d", upsEdges[0].From, upsEdges[0].FromPort)
	}
	if upsEdges[0].Source != "seen" {
		t.Errorf("a seen attachment must stay marked as inference, got %q", upsEdges[0].Source)
	}
}
