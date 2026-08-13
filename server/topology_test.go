package server

import (
	"encoding/json"
	"testing"
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
	top := unifiTopology(decodeRack(t))
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
	top := unifiTopology(decodeRack(t))
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
	top := unifiTopology(decodeRack(t))
	byMAC := map[string]TopoNode{}
	for _, n := range top.Nodes {
		byMAC[n.MAC] = n
	}
	// The core switch has three port entries, one of which is the access
	// switch — already a node, and drawn as a line rather than counted twice.
	if got := byMAC["8c:30:66:d0:6e:43"].Leaves; got != 3 {
		t.Errorf("core leaves = %d", got)
	}
	// None of those addresses became nodes.
	for _, n := range top.Nodes {
		if n.MAC == "aa:bb:cc:00:00:01" {
			t.Error("a leaf address was drawn as a device")
		}
	}
}

// A neighbour reported by LLDP that is not itself on the map is not a line: it
// would be an edge to nowhere, drawn with the same weight as a real one.
func TestNeighboursOffTheMapAreNotDrawn(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[{"mac":"aa:aa:aa:aa:aa:aa","name":"lone","type":"usw","state":1,
	  "lldp_table":[{"chassis_id":"ff:ff:ff:ff:ff:ff","local_port_idx":1}]}]}`), &v)
	top := unifiTopology(v)
	if len(top.Edges) != 0 {
		t.Errorf("drew an edge to a device that is not on the map: %+v", top.Edges)
	}
	if len(top.Nodes) != 1 {
		t.Errorf("nodes = %+v", top.Nodes)
	}
}
