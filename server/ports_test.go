package server

import (
	"encoding/json"
	"testing"
)

// Trimmed from a real controller answer: a live port with a named device and
// PoE, a port whose device has gone away, an uplink, and an empty one.
const switchDoc = `{"data":[
 {"mac":"aa:aa:aa:aa:aa:aa","port_table":[{"port_idx":1,"name":"other switch"}]},
 {"mac":"d8:b3:70:83:96:77","model":"US24P250","port_table":[
   {"port_idx":2,"name":"Access Point","media":"GE","up":true,"speed":1000,
    "poe_enable":true,"poe_good":true,"poe_power":"7.85","bytes-r":21990.69,
    "last_connection":{"mac":"EC:C9:FF:96:D3:48","last_seen":1786581855}},
   {"port_idx":4,"name":"Port 4","media":"GE","up":false,"speed":0,
    "poe_enable":false,"poe_good":false,"poe_power":"0.00",
    "last_connection":{"mac":"00:c0:b7:93:81:00","last_seen":1776958900}},
   {"port_idx":3,"name":"Port 3","media":"GE","up":false},
   {"port_idx":25,"name":"Switch Uplink","media":"SFP","up":true,"speed":1000,
    "bytes-r":21662.44,"last_connection":{"mac":"8c:30:66:d0:6e:43"}}]}]}`

func decodeSwitch(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(switchDoc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPortTableIsReadForTheRightSwitch(t *testing.T) {
	ports, ok := unifiPorts(decodeSwitch(t), "d8:b3:70:83:96:77")
	if !ok {
		t.Fatal("no port table found")
	}
	if len(ports) != 4 {
		t.Fatalf("got %d ports: %+v", len(ports), ports)
	}
	// Sorted by port number, not by the order the controller happened to use —
	// port 3 comes after 2 in the answer's own listing but before 4.
	if ports[0].Index != 2 || ports[1].Index != 3 || ports[2].Index != 4 || ports[3].Index != 25 {
		t.Errorf("order = %d %d %d %d", ports[0].Index, ports[1].Index, ports[2].Index, ports[3].Index)
	}

	ap := ports[0]
	if ap.Name != "Access Point" || !ap.Up || ap.Speed != 1000 {
		t.Errorf("port 2 = %+v", ap)
	}
	// PoE arrives quoted, like every other number this controller reports.
	if !ap.PoEOn || ap.PoEWatts != 7.85 {
		t.Errorf("PoE on port 2 = %v %v", ap.PoEOn, ap.PoEWatts)
	}
	if len(ap.Peers) != 1 || ap.Peers[0].MAC != "ec:c9:ff:96:d3:48" {
		t.Fatalf("peer on port 2 = %+v", ap.Peers)
	}
	// Lower-cased on the way in: the controller shouts some of them and the
	// hub's MAC index does not, and a case difference would silently fail to
	// resolve into a name.
	if ap.Peers[0].Stale {
		t.Error("a linked port was reported as stale")
	}

	// A port whose device has gone: the switch still remembers what was there,
	// and that is more interesting than a port that was always empty.
	gone := ports[2]
	if len(gone.Peers) != 1 || !gone.Peers[0].Stale {
		t.Errorf("port 4 = %+v", gone)
	}
	// A port that has never had anything on it says nothing.
	if len(ports[1].Peers) != 0 {
		t.Errorf("port 3 invented a peer: %+v", ports[1].Peers)
	}
}

// The controller answers for every device it manages, so the MAC is what picks
// one out — and a wrong one must fail rather than return another switch's
// ports, which would be a confidently wrong answer about the wrong hardware.
func TestPortTableNeedsTheRightMAC(t *testing.T) {
	if _, ok := unifiPorts(decodeSwitch(t), "00:00:00:00:00:00"); ok {
		t.Error("an unknown MAC returned a port table")
	}
	// Case and spacing are the operator's, not a format they have to match.
	if _, ok := unifiPorts(decodeSwitch(t), " D8:B3:70:83:96:77 "); !ok {
		t.Error("a MAC typed in upper case did not match")
	}
	// A device with no ports at all is not a switch; say so rather than
	// returning an empty table that looks like a switch with nothing plugged in.
	var v any
	json.Unmarshal([]byte(`{"data":[{"mac":"aa:bb:cc:dd:ee:ff","num_sta":3}]}`), &v)
	if _, ok := unifiPorts(v, "aa:bb:cc:dd:ee:ff"); ok {
		t.Error("a device with no port table was treated as a switch")
	}
}

// A MAC is an opaque number until something names it. The hub already holds
// three ways to name one, and the point of the port map is that an unnamed port
// is then genuinely unknown rather than merely unlabelled.
func TestPeersAreNamedFromWhatTheHubAlreadyKnows(t *testing.T) {
	s := &Server{netChecks: newNetChecks(t.TempDir())}
	s.netChecks.checks["ap"] = &NetCheck{ID: "ap", Name: "Access Point - Basement", MAC: "ec:c9:ff:96:d3:48"}
	s.netChecks.macs.byMAC["ec:c9:ff:96:d3:48"] = "10.0.0.253"

	ports, _ := unifiPorts(decodeSwitch(t), "d8:b3:70:83:96:77")
	pm := PortMap{Ports: ports}
	s.resolvePeers(&pm)

	named := pm.Ports[0].Peers[0]
	if named.Name != "Access Point - Basement" {
		t.Errorf("name = %q, want the device it is", named.Name)
	}
	if named.IP != "10.0.0.253" {
		t.Errorf("ip = %q", named.IP)
	}
	// Something the hub has never heard of still gets its manufacturer, which
	// is the difference between "unknown" and "some Espressif thing on port 9".
	unknown := pm.Ports[2].Peers[0]
	if unknown.Name != "" {
		t.Errorf("an unknown MAC was given the name %q", unknown.Name)
	}
	if unknown.Vendor == "" {
		t.Errorf("no vendor for %s", unknown.MAC)
	}
}
