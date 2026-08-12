package server

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network discovery.
//
// The hub already sweeps its local networks to resolve MAC-tracked devices, so
// it already knows what is on the LAN — it just never said so. This lists what
// the sweep found, marks what is already monitored, and lets the rest be added
// without anyone reading an IP off a router page and typing it back in.
//
// Deliberately the hub's own networks only: this reads the ARP table, which
// does not cross a router. Reporting hosts beyond that would need scanning
// somebody else's network, which is not a thing this should do quietly.

// Discovered is one device seen on the network.
type Discovered struct {
	IP  string `json:"ip"`
	MAC string `json:"mac"`
	// Name is reverse DNS, when it answers. Most homelab devices have no
	// record, so empty is the normal case rather than a failure.
	Name string `json:"name,omitempty"`
	// Monitored marks a device the hub already watches or has an agent on.
	Monitored bool   `json:"monitored"`
	Why       string `json:"why,omitempty"`   // what it is already known as
	Ports     []int  `json:"ports,omitempty"` // open ports, to hint at what it is
}

// discoverPorts is what a discovered device is probed on: enough to guess what
// something is, few enough to stay quick across a whole subnet.
var discoverPorts = []int{80, 443, 22, 445, 9100, 8006, 5000, 8080}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	// A sweep is what makes devices the hub has not spoken to appear at all.
	idx := s.macIndex()
	if idx == nil {
		http.Error(w, "network checks are not configured on this hub", http.StatusConflict)
		return
	}
	idx.refresh(ctx, true)
	table := idx.snapshot()

	known, why := s.knownAddresses()

	out := make([]Discovered, 0, len(table))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for mac, ip := range table {
		wg.Add(1)
		go func(mac, ip string) {
			defer wg.Done()
			d := Discovered{IP: ip, MAC: mac, Name: reverseName(ctx, ip), Ports: openPorts(ctx, ip)}
			if k := known[ip]; k {
				d.Monitored, d.Why = true, why[ip]
			}
			mu.Lock()
			out = append(out, d)
			mu.Unlock()
		}(mac, ip)
	}
	wg.Wait()

	// Unmonitored first — those are the ones there is anything to do about —
	// then by address so the list does not reshuffle between sweeps.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Monitored != out[j].Monitored {
			return !out[i].Monitored
		}
		return lessIP(out[i].IP, out[j].IP)
	})
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// macIndex is the address table the netcheck machinery keeps.
func (s *Server) macIndex() *macIndex {
	if s.netChecks == nil {
		return nil
	}
	return s.netChecks.macs
}

// knownAddresses maps every address the hub already watches to what it is.
func (s *Server) knownAddresses() (map[string]bool, map[string]string) {
	known := map[string]bool{}
	why := map[string]string{}
	if s.netChecks != nil {
		for _, st := range s.netChecks.list() {
			addr := st.Address
			if st.IP != "" {
				addr = st.IP
			}
			if addr != "" {
				known[addr] = true
				why[addr] = "monitored as " + st.Name
			}
		}
	}
	// An enrolled host is not something to add as a network device.
	for _, v := range s.store.views() {
		for _, ip := range v.Facts.IPs {
			if ip != "" {
				known[ip] = true
				why[ip] = "agent installed (" + v.Hostname + ")"
			}
		}
	}
	return known, why
}

// reverseName asks DNS what this address is called. Most homelab devices have
// no record, so an empty answer is the normal case rather than a failure.
func reverseName(ctx context.Context, ip string) string {
	c, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(c, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// openPorts reports which of a short list answer, to hint at what a device is.
func openPorts(ctx context.Context, ip string) []int {
	var mu sync.Mutex
	var open []int
	var wg sync.WaitGroup
	for _, p := range discoverPorts {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			d := net.Dialer{Timeout: 600 * time.Millisecond}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(p)))
			if err != nil {
				return
			}
			conn.Close()
			mu.Lock()
			open = append(open, p)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Ints(open)
	return open
}

// lessIP orders addresses numerically, so .9 comes before .10.
func lessIP(a, b string) bool {
	ipa, ipb := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if ipa == nil || ipb == nil {
		return a < b
	}
	for i := range ipa {
		if ipa[i] != ipb[i] {
			return ipa[i] < ipb[i]
		}
	}
	return false
}
