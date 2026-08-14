package server

import (
	"encoding/json"
	"testing"
)

// Which ports carry the traffic, from the document already being read. The
// distinction that matters: a busy uplink is everything beyond it, not one
// machine, and ranking it beside access ports without saying so would put
// "the whole rack" at #1 dressed as a device.
func TestTalkersRankPortsAndMarkUplinks(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":[
	 {"mac":"aa:aa:aa:aa:aa:aa","name":"core","type":"usw",
	  "port_table":[
	    {"port_idx":4,"name":"HL-PC1","bytes-r":324808.9,"last_connection":{"mac":"e0:d4:e8:5c:5d:1a"}},
	    {"port_idx":24,"name":"Port 24","bytes-r":792018.8,"last_connection":{"mac":"64:62:66:23:c2:28"}},
	    {"port_idx":28,"name":"Switch Uplink","bytes-r":27223.4,"last_connection":{"mac":"bb:bb:bb:bb:bb:bb"}},
	    {"port_idx":9,"name":"Synology 1","bytes-r":2239.0,"last_connection":{"mac":"90:09:d0:58:9a:ca"}},
	    {"port_idx":13,"name":"Port 13","bytes-r":0}]},
	 {"mac":"bb:bb:bb:bb:bb:bb","name":"access","type":"usw",
	  "uplink":{"uplink_mac":"aa:aa:aa:aa:aa:aa","uplink_remote_port":28,"port_idx":25,"speed":1000},
	  "port_table":[
	    {"port_idx":2,"name":"AP","bytes-r":21990.7,"last_connection":{"mac":"ec:c9:ff:96:d3:48"}},
	    {"port_idx":25,"name":"Uplink","bytes-r":21662.4,"last_connection":{"mac":"aa:aa:aa:aa:aa:aa"}}]}]}`), &v)

	got := controllerTalkers(v)
	if len(got) != 6 {
		t.Fatalf("got %d talkers: %+v", len(got), got) // the idle port is not one
	}
	// The busiest access port is a machine; the port to the gateway and the
	// inter-switch ports are not.
	byPort := map[string]Talker{}
	for _, tk := range got {
		byPort[tk.Device+"#"+strconvI(tk.Port)] = tk
	}
	if tk := byPort["core#4"]; tk.Uplink || len(tk.Peers) != 1 {
		t.Errorf("an access port was marked uplink: %+v", tk)
	}
	if tk := byPort["core#28"]; !tk.Uplink {
		t.Error("the inter-switch port was not marked as an uplink")
	}
	if tk := byPort["access#25"]; !tk.Uplink {
		t.Error("the other end of the same cable was not marked either")
	}
}

func strconvI(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
