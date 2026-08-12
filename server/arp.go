package server

import (
	"context"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/procattr"
)

// Finding devices that move.
//
// A DHCP device has no address worth writing down, so autormm lets one be
// identified by MAC instead and works out where it is now. The hub shares a LAN
// with them, so its own ARP table already holds the answer for anything it has
// spoken to recently — and a quick sweep provokes entries for anything it has
// not.

// macIndex maps MAC addresses to the IP each currently holds.
type macIndex struct {
	mu      sync.RWMutex
	byMAC   map[string]string
	updated time.Time
	swept   time.Time
}

// minSweep bounds how often the hub will provoke ARP entries. A device that is
// switched off never resolves, so without this the steady state for one
// powered-down machine is a full sweep on every check cycle, forever.
const minSweep = time.Minute

// freshFor is how long a read of the ARP table stands without a re-read, when
// nothing is being searched for. The check tick is faster than this.
const freshFor = 30 * time.Second

func newMACIndex() *macIndex { return &macIndex{byMAC: map[string]string{}} }

func (m *macIndex) lookup(mac string) (string, bool) {
	mac = normalizeMAC(mac)
	if mac == "" {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ip, ok := m.byMAC[mac]
	return ip, ok
}

// snapshot copies the current MAC to address mapping.
func (m *macIndex) snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.byMAC))
	for k, v := range m.byMAC {
		out[k] = v
	}
	return out
}

// macFor is the reverse lookup: which MAC currently holds this address.
func (m *macIndex) macFor(ip string) string {
	if ip == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for mac, addr := range m.byMAC {
		if addr == ip {
			return mac
		}
	}
	return ""
}

// refresh rebuilds the index. sweep provokes ARP entries for devices the hub
// has not spoken to lately, which is most of them — it is only worth the
// traffic when something is actually being looked for.
func (m *macIndex) refresh(ctx context.Context, sweep bool) {
	m.mu.RLock()
	fresh := !m.updated.IsZero() && time.Since(m.updated) < freshFor
	m.mu.RUnlock()
	if !sweep && fresh {
		return // nothing to gain from re-reading the table this often
	}

	m.mu.Lock()
	if sweep && time.Since(m.swept) < minSweep {
		sweep = false
	} else if sweep {
		m.swept = time.Now()
	}
	m.mu.Unlock()
	if sweep {
		sweepLocalNets(ctx)
	}
	table := readARP()
	m.mu.Lock()
	// Replace wholesale rather than merging: a stale entry for a device that
	// has since changed address is worse than none, because it sends checks to
	// whatever holds that address now.
	m.byMAC = table
	m.updated = time.Now()
	m.mu.Unlock()
}

// normalizeMAC returns a MAC in lowercase colon form, or "" if it is not one.
//
// Accepts the separators people type and, importantly, the short octets that
// macOS prints: `arp -a` there renders a leading zero away, giving
// "0:11:22:aa:bb:cc", which ParseMAC rejects outright. Padding first is the
// difference between reading that table and silently reading none of it.
func normalizeMAC(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	if s == "" {
		return ""
	}
	sep := ":"
	if !strings.Contains(s, ":") && strings.Contains(s, "-") {
		sep = "-"
	}
	if parts := strings.Split(s, sep); len(parts) == 6 {
		for i, p := range parts {
			if len(p) == 1 {
				parts[i] = "0" + p
			}
		}
		s = strings.Join(parts, sep)
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return ""
	}
	return strings.ToLower(hw.String())
}

// arpLine matches an IPv4 address and a MAC anywhere on the same line, which is
// the one thing every `arp -a` format on every platform agrees about.
var arpLine = regexp.MustCompile(`(\d{1,3}(?:\.\d{1,3}){3}).*?((?:[0-9a-fA-F]{1,2}[:-]){5}[0-9a-fA-F]{1,2})`)

// readARP returns the current neighbour table as MAC -> IP.
func readARP() map[string]string {
	if t := readProcARP(); len(t) > 0 {
		return t
	}
	return readARPCommand()
}

// readProcARP parses Linux's /proc/net/arp, which is stable and needs no
// subprocess. Incomplete entries are skipped: the kernel lists an address it
// has tried and failed to resolve with an all-zero MAC and no complete flag,
// and treating that as a device would invent one.
func readProcARP() map[string]string {
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 {
			continue // header
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(f[2], "0x"), 16, 32)
		if err != nil || flags&0x2 == 0 { // ATF_COM: resolved
			continue
		}
		mac := normalizeMAC(f[3])
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		out[mac] = f[0]
	}
	return out
}

// readARPCommand is the fallback for hubs that are not Linux.
func readARPCommand() map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "arp", "-a")
	procattr.Hide(cmd)
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range arpLine.FindAllStringSubmatch(string(b), -1) {
		mac := normalizeMAC(m[2])
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		out[mac] = m[1]
	}
	return out
}

// sweepableNets returns the local IPv4 networks small enough to walk.
//
// Anything larger than a /22 is skipped: a /16 is 65,000 addresses, and the
// container bridges that typically carry one hold nothing worth finding.
func sweepableNets() []*net.IPNet {
	var out []*net.IPNet
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
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			if ones, bits := ipn.Mask.Size(); bits != 32 || ones < 22 {
				continue
			}
			out = append(out, ipn)
		}
	}
	return out
}

// sweepLocalNets provokes ARP resolution for every address on the local
// networks, so devices the hub has never spoken to still appear in the table.
//
// A TCP connection attempt is enough: the kernel must resolve the MAC before it
// can send anything, so even a refused or ignored connection leaves an entry.
// That is the whole point — the reply is irrelevant and never read.
func sweepLocalNets(ctx context.Context) {
	const (
		probePort   = 80
		perHost     = 300 * time.Millisecond
		concurrency = 64
	)
	// Under the check tick, so a sweep can never make the hub skip a round of
	// checks. A /24 at this concurrency finishes in about a second anyway; the
	// budget only bounds a pathological network.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, n := range sweepableNets() {
		for ip := n.IP.Mask(n.Mask).To4(); n.Contains(ip); ip = nextIP(ip) {
			target := net.JoinHostPort(ip.String(), strconv.Itoa(probePort))
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				d := net.Dialer{Timeout: perHost}
				if c, err := d.DialContext(ctx, "tcp", target); err == nil {
					c.Close()
				}
			}()
			if ctx.Err() != nil {
				break
			}
		}
	}
	wg.Wait()
}

// nextIP returns the following address, on a copy.
func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}
