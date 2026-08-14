package server

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Transcribed from a real controller answer (addresses genericised) — every
// relation below appears in that dump: the legacy uplink with no far end, the
// router visible as two interfaces one octet apart, offline devices with stale
// uplinks, and devices whose addresses appear on transit ports. This is the
// rack the map kept getting wrong when tested only against invented fixtures.
const realRack = `{"data":[
 {"mac":"8c:30:66:d0:6e:43","name":"USW Pro HD 24","type":"usw","state":1,"ip":"10.0.0.245",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"mac":"8c:30:66:d0:6e:43","port_idx":24,"speed":2500,"uplink_source":"legacy"},
  "lldp_table":[{"chassis_id":"d8:b3:70:1b:6c:2b","local_port_idx":19},
                {"chassis_id":"58:d6:1f:1a:5b:99","local_port_idx":20},
                {"chassis_id":"64:62:66:23:c2:25","local_port_idx":27},
                {"chassis_id":"d8:b3:70:83:96:77","local_port_idx":28}],
  "port_table":[
    {"port_idx":1,"speed":2500,"up":true,"last_connection":{"mac":"d8:bb:c1:a5:ee:5f"}},
    {"port_idx":2,"speed":2500,"up":true,"last_connection":{"mac":"04:42:1a:f0:aa:93"}},
    {"port_idx":3,"speed":1000,"up":true,"last_connection":{"mac":"00:1f:bc:11:c6:32"}},
    {"port_idx":4,"speed":2500,"up":true,"last_connection":{"mac":"e0:d4:e8:5c:5d:1a"}},
    {"port_idx":5,"speed":1000,"up":true,"last_connection":{"mac":"6c:4b:90:68:0b:df"}},
    {"port_idx":8,"speed":1000,"up":true,"last_connection":{"mac":"ec:e7:a7:1d:85:c0"}},
    {"port_idx":9,"speed":1000,"up":true,"last_connection":{"mac":"90:09:d0:58:9a:ca"}},
    {"port_idx":10,"speed":1000,"up":true,"last_connection":{"mac":"90:09:d0:58:9a:c9"}},
    {"port_idx":11,"speed":1000,"up":true,"last_connection":{"mac":"90:09:d0:58:9a:c8"}},
    {"port_idx":12,"speed":1000,"up":true,"last_connection":{"mac":"90:09:d0:58:9a:c7"}},
    {"port_idx":17,"speed":100,"up":true,"last_connection":{"mac":"b8:a0:b8:4d:e9:84"}},
    {"port_idx":18,"speed":1000,"up":true,"last_connection":{"mac":"78:20:a5:8c:ac:f6"}},
    {"port_idx":19,"speed":100,"up":true,"last_connection":{"mac":"d8:b3:70:1b:6c:2b"}},
    {"port_idx":20,"speed":100,"up":true,"last_connection":{"mac":"58:d6:1f:1a:5b:99"}},
    {"port_idx":21,"speed":1000,"up":true,"last_connection":{"mac":"28:29:86:6f:a1:d5"}},
    {"port_idx":22,"speed":1000,"up":true,"last_connection":{"mac":"28:29:86:92:b9:e4"}},
    {"port_idx":23,"speed":10000,"up":true,"last_connection":{"mac":"30:c5:99:5d:7b:3b"}},
    {"port_idx":24,"speed":2500,"up":true,"last_connection":{"mac":"64:62:66:23:c2:28"}},
    {"port_idx":27,"speed":10000,"up":true,"last_connection":{"mac":"64:62:66:23:c2:25"}},
    {"port_idx":28,"speed":1000,"up":true,"last_connection":{"mac":"d8:b3:70:83:96:77"}}]},
 {"mac":"d8:b3:70:83:96:77","name":"US 24 PoE 250W","type":"usw","state":1,"ip":"10.0.0.246",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":28,"port_idx":25,"speed":1000,
            "uplink_device_name":"USW Pro HD 24"},
  "lldp_table":[{"chassis_id":"8c:30:66:80:95:7c","local_port_idx":2},
                {"chassis_id":"8c:30:66:d0:6e:43","local_port_idx":25}],
  "port_table":[
    {"port_idx":2,"speed":1000,"up":true,"last_connection":{"mac":"ec:c9:ff:96:d3:48"}},
    {"port_idx":4,"speed":100,"up":true,"last_connection":{"mac":"00:c0:b7:93:81:00"}},
    {"port_idx":23,"speed":100,"up":true,"last_connection":{"mac":"f8:5a:00:33:e4:57"}},
    {"port_idx":25,"speed":1000,"up":true,"last_connection":{"mac":"8c:30:66:d0:6e:43"}}]},
 {"mac":"8c:30:66:80:95:7c","name":"Access Point - Basement","type":"uap","state":1,"ip":"10.0.0.253",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"d8:b3:70:83:96:77","uplink_remote_port":2,"port_idx":1,"speed":1000}},
 {"mac":"d8:b3:70:1b:6c:2b","name":"TOP USP PDU Pro","type":"usw","state":1,"ip":"10.0.0.247",
  "gateway_mac":"64:62:66:23:c2:28","outlet_table":[{"index":1}],
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":19,"port_idx":1,"speed":100}},
 {"mac":"58:d6:1f:1a:5b:99","name":"Bottom USP PDU Pro","type":"usw","state":1,"ip":"10.0.0.248",
  "gateway_mac":"64:62:66:23:c2:28","outlet_table":[{"index":1}],
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":20,"port_idx":1,"speed":100}},
 {"mac":"ac:8b:a9:58:f7:b2","name":"USW Lite 16 PoE","type":"usw","state":0,"ip":"10.0.0.137",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"8c:30:66:d0:6e:43","uplink_remote_port":14,"port_idx":16,"type":"wire"}},
 {"mac":"68:d7:9a:cc:d2:75","name":"AC LR","type":"uap","state":0,"ip":"10.0.0.11",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"ac:8b:a9:58:f7:b2","uplink_remote_port":2,"port_idx":1,"type":"wire"}},
 {"mac":"f4:e2:c6:40:ff:22","name":"U6+","type":"uap","state":0,"ip":"10.0.0.115",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"d8:b3:70:83:96:77","uplink_remote_port":6,"port_idx":1,"type":"wire"}},
 {"mac":"8c:30:66:80:9f:0c","name":"U7 Pro XG","type":"uap","state":0,"ip":"10.0.0.49",
  "gateway_mac":"64:62:66:23:c2:28",
  "uplink":{"uplink_mac":"d8:b3:70:83:96:77","uplink_remote_port":24,"port_idx":1,"type":"wire"}}]}`

func TestRealRackHasOneRouterAndOneLinePerDevice(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(realRack), &v); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: NewStore(60, time.Minute, nil), netChecks: newNetChecks(t.TempDir())}
	add := func(id, name, mac string) {
		c := NetCheck{ID: id, Name: name, MAC: mac}
		s.netChecks.checks[id] = &c
		s.netChecks.state[id] = &NetStatus{NetCheck: c, Up: true}
	}
	add("fw", "firewall", "64:62:66:23:c2:28")
	add("ups1", "Top UPS", "28:29:86:6f:a1:d5")
	add("ups2", "Bottom UPS", "28:29:86:92:b9:e4")
	add("prn", "HP Printer", "b8:a0:b8:4d:e9:84")
	s.store.register(protocol.Register{AgentID: "tron", Hostname: "TRON",
		Facts: protocol.HostFacts{MACs: []string{"d8:bb:c1:a5:ee:5f"}}}, nil)
	s.store.register(protocol.Register{AgentID: "hl", Hostname: "HL-PC1",
		Facts: protocol.HostFacts{MACs: []string{"e0:d4:e8:5c:5d:1a"}}}, nil)

	top, unclaimed := unifiTopology(v)
	s.attachKnown(&top, unclaimed)
	s.linkToChecks(&top)

	// The router's two interfaces are one box, named by the check that owns it.
	routers := 0
	for _, n := range top.Nodes {
		if n.MAC == "64:62:66:23:c2:25" || n.MAC == "64:62:66:23:c2:28" {
			routers++
			if n.Name != "firewall" {
				t.Errorf("router named %q", n.Name)
			}
		}
	}
	if routers != 1 {
		t.Fatalf("the router appears %d times", routers)
	}

	// One line per device: nothing on this map earns two.
	lines := map[string]int{}
	for _, e := range top.Edges {
		lines[e.From]++
		lines[e.To]++
	}
	for _, n := range top.Nodes {
		max := 1
		switch n.MAC {
		case "8c:30:66:d0:6e:43": // the core feeds everything
			max = 99
		case "d8:b3:70:83:96:77": // the access switch feeds the APs
			max = 99
		case "ac:8b:a9:58:f7:b2": // the offline switch still feeds AC LR
			max = 2
		}
		if lines[n.MAC] > max {
			t.Errorf("%s (%s) has %d lines", n.Name, n.MAC, lines[n.MAC])
		}
	}

	// The offline chain survives: Lite 16 under the core, AC LR under it.
	found := map[string]bool{}
	for _, e := range top.Edges {
		found[e.From+">"+e.To] = true
	}
	if !found["8c:30:66:d0:6e:43>ac:8b:a9:58:f7:b2"] {
		t.Error("the offline Lite 16's stale uplink is missing")
	}
	if !found["ac:8b:a9:58:f7:b2>68:d7:9a:cc:d2:75"] {
		t.Error("AC LR should hang off the Lite 16")
	}

	b, _ := json.Marshal(top)
	os.WriteFile("/tmp/topo_real.json", b, 0o644)
	t.Logf("nodes=%d edges=%d root=%s", len(top.Nodes), len(top.Edges), top.Root)
}
