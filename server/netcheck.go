package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
)

// Agentless monitoring for things that cannot run an agent: switches, printers,
// IoT, a NAS appliance, the Twingate connector. Half of a homelab is not a
// computer you can install software on, and until now none of it was visible.
//
// A check is a TCP connect, or an ICMP-free "ping" — deliberately TCP, because
// raw ICMP needs privileges the hub should not hold, and "port 443 answers" is
// a better statement about a device than "it replies to ping" anyway.
type NetCheck struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`       // host or IP
	Port     int    `json:"port"`          // 0 => try a small set of common ports
	Interval int    `json:"interval_secs"` // 0 => default
	Tags     string `json:"tags,omitempty"`
	// URL of the device's own management interface. Blank means "work it out
	// from the address and port", which is right for most things and wrong for
	// the ones that answer on one port and serve their UI on another.
	URL string `json:"url,omitempty"`
	// Kind is "app" for a hosted web application, or empty for a device. Apps
	// are checked over HTTP rather than by opening a socket, because "the port
	// is open" says almost nothing about whether an application is serving.
	Kind string `json:"kind,omitempty"`
	// SNMP is the v2c community string. Empty disables polling, which is the
	// default: a community string is a credential, however weak, and nothing
	// should be sending one anywhere it was not told to.
	SNMP string `json:"snmp,omitempty"`
	// SNMPPort overrides the standard 161.
	SNMPPort int `json:"snmp_port,omitempty"`
	// MAC identifies a device that gets its address from DHCP, where writing
	// down an IP is writing down something that will change. When set, the
	// address is looked up from the hub's ARP table at check time and Address
	// is only a fallback for before the first lookup succeeds.
	MAC string `json:"mac,omitempty"`
}

// IsApp reports whether this entry is a hosted application rather than a device.
func (c NetCheck) IsApp() bool { return c.Kind == "app" }

// WebURL is where a device's own control panel lives, so a card can open it.
//
// An explicit URL always wins. Otherwise it is inferred from the port being
// probed, which is usually the management interface — that is why those ports
// were chosen as the defaults to probe in the first place.
//
// Some ports have no web interface at all, and guessing one would send the
// operator to a browser error: SSH, raw printing and SMB return nothing, and
// their cards simply are not clickable.
func (c NetCheck) WebURL() string {
	if c.URL != "" {
		// A URL written without a scheme is displayed as https: it is the safer
		// of the two to offer, and the probe will correct it to http if that is
		// what the app actually answers on.
		return appCandidates(c.URL)[0]
	}
	switch c.Port {
	case 22, 445, 9100, 139, 3389:
		return "" // not web interfaces; a link would only mislead
	case 0, 80:
		// No port configured means the probe tries several; http is the right
		// first guess for a device whose management port is unknown.
		return "http://" + c.Address
	case 443:
		return "https://" + c.Address
	case 8006, 8443, 9443, 10000:
		return "https://" + net.JoinHostPort(c.Address, strconv.Itoa(c.Port))
	default:
		return "http://" + net.JoinHostPort(c.Address, strconv.Itoa(c.Port))
	}
}

// NetStatus is the last observed state of a check.
type NetStatus struct {
	NetCheck
	// WebURL is resolved server-side so the dashboard does not have to repeat
	// the port-to-scheme reasoning, and so both agree on what a card links to.
	Web string `json:"web,omitempty"`
	// Code is the HTTP status an app answered with, so a card can tell
	// "serving" from "answering with 502".
	Code int `json:"code,omitempty"`
	// IP is where a MAC-identified device was found this time round.
	IP string `json:"ip,omitempty"`
	// SNMP is the last successful poll, kept across cycles so a single missed
	// reply does not blank the card.
	SNMP      *SNMPInfo `json:"snmp,omitempty"`
	Up        bool      `json:"up"`
	LatencyMs float64   `json:"latency_ms,omitempty"`
	Since     time.Time `json:"since"` // when the current state began
	Checked   time.Time `json:"checked"`
	Error     string    `json:"error,omitempty"`
}

const (
	defaultCheckInterval = 60 * time.Second
	checkTimeout         = 4 * time.Second
)

// commonPorts is what a device with no port specified is probed on. Between
// them these cover management interfaces, printers and appliances; the first to
// answer wins.
var commonPorts = []int{80, 443, 22, 9100, 445, 8006}

// netChecks stores the configured checks and their latest state.
type netChecks struct {
	mu     sync.RWMutex
	path   string
	checks map[string]*NetCheck
	state  map[string]*NetStatus
	macs   *macIndex
}

// hasMACs reports whether any of these checks is identified by MAC.
func hasMACs(list []NetCheck) bool {
	for _, c := range list {
		if c.MAC != "" {
			return true
		}
	}
	return false
}

func newNetChecks(dir string) *netChecks {
	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}, macs: newMACIndex()}
	if dir != "" {
		n.path = filepath.Join(dir, "netchecks.json")
		if b, err := os.ReadFile(n.path); err == nil {
			var list []NetCheck
			if json.Unmarshal(b, &list) == nil {
				for i := range list {
					c := list[i]
					n.checks[c.ID] = &c
				}
			}
		}
	}
	return n
}

func (n *netChecks) save() error {
	n.mu.RLock()
	list := make([]NetCheck, 0, len(n.checks))
	for _, c := range n.checks {
		list = append(list, *c)
	}
	path := n.path
	n.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	if path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

func (n *netChecks) list() []NetStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]NetStatus, 0, len(n.checks))
	for id, c := range n.checks {
		if st := n.state[id]; st != nil {
			s := *st
			s.Web = s.NetCheck.WebURL()
			out = append(out, s)
			continue
		}
		out = append(out, NetStatus{NetCheck: *c, Web: c.WebURL()}) // not yet probed
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// probe attempts a connection and reports whether the device answered.
//
// A *refused* connection still proves the device is there: something sent back
// a rejection, which means the host is up and merely not listening on that
// port. Treating that as down would cry wolf on every device whose port guess
// was wrong — and guessing is exactly what happens when no port is configured.
// Silence (a timeout) is the only thing that means gone.
func probe(ctx context.Context, address string, port int) (up bool, ms float64, answered int, err error) {
	ports := []int{port}
	if port == 0 {
		ports = commonPorts
	}
	// A refusal proves the host is alive, but says nothing about where its
	// interface lives — so when several ports are in play it is remembered and
	// the scan continues. Stopping at the first refusal is how a firewall that
	// rejects 80 and serves on 443 got recorded as "up on port 80", and then
	// linked to http://host, which opens a blank page.
	sawRefused, refusedMs := false, float64(0)
	for _, p := range ports {
		start := time.Now()
		d := net.Dialer{Timeout: checkTimeout}
		conn, dialErr := d.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(p)))
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if dialErr == nil {
			conn.Close()
			return true, elapsed, p, nil
		}
		err = dialErr
		if refused(dialErr) && !sawRefused {
			sawRefused, refusedMs = true, elapsed
		}
	}
	if sawRefused {
		return true, refusedMs, 0, nil
	}
	return false, 0, 0, err
}

// urlFor writes the URL for a scheme and port, leaving the port off when it is
// that scheme's default so the link reads the way an operator would type it.
func urlFor(scheme, host string, port int) string {
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// deviceCandidates lists the URLs worth trying for a device found on a port,
// most likely first. Nil means this port serves no web interface.
func deviceCandidates(c NetCheck, port int) []string {
	if c.URL != "" {
		return appCandidates(c.URL)
	}
	if port == 0 {
		port = c.Port
	}
	switch port {
	case 0, 22, 139, 445, 3389, 9100:
		return nil // not web interfaces; a link would only mislead
	case 80, 8000, 8080:
		return []string{urlFor("http", c.Address, port), urlFor("https", c.Address, port)}
	}
	// Everything else leads with https. Appliances that serve plaintext on an
	// unusual port are rarer than ones that serve TLS, and the fallback costs
	// one request on a check that already runs a minute apart.
	return []string{urlFor("https", c.Address, port), urlFor("http", c.Address, port)}
}

// deviceWebURL asks the device which scheme it actually serves instead of
// inferring one from the port number, and returns "" if it serves neither.
//
// The inference was wrong often enough to matter: an appliance answering TLS on
// 8080, or anything reached through the common-port scan, produced a link that
// opened a blank page. Trying both is a fact where the port number was a guess.
func deviceWebURL(ctx context.Context, c NetCheck, port int) string {
	for _, u := range deviceCandidates(c, port) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "autormm-healthcheck")
		resp, err := appClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		return u
	}
	return ""
}

// appClient checks that an application is serving, without judging what it
// serves.
//
// Redirects are not followed: a 302 to a login page is a perfectly healthy
// application, and chasing it only risks wandering off to an identity provider.
// Certificates are not verified either — homelab applications routinely carry
// self-signed ones, and refusing to look would report every one of them as down
// while proving nothing: this is a liveness check, and nothing it receives is
// trusted or shown.
var appClient = &http.Client{
	Timeout: checkTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: checkTimeout,
	},
}

// appCandidates lists the URLs worth trying for what the operator wrote, most
// likely first.
//
// A scheme they supplied is respected absolutely. Without one, https is tried
// before http: an app that only serves https fails outright over http, whereas
// one that only serves http usually redirects — so guessing https costs a
// retry at worst, and guessing http can mark a working app as down. That is
// what forcing http:// on a bare hostname used to do.
func appCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return []string{raw}
	}
	return []string{"https://" + raw, "http://" + raw}
}

// probeApp reports whether an application answered, and with what.
//
// Any HTTP response counts as reachable, including 401, 403 and 500: the
// application is running and talking. Only a transport failure — refused,
// timed out, DNS gone — means it is not there. Treating 401 as down would mark
// every authenticated app in a homelab as broken.
func probeApp(ctx context.Context, rawURL string) (up bool, ms float64, code int, used string, err error) {
	candidates := appCandidates(rawURL)
	for _, u := range candidates {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if reqErr != nil {
			err = reqErr
			continue
		}
		req.Header.Set("User-Agent", "autormm-healthcheck")
		start := time.Now()
		resp, doErr := appClient.Do(req)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if doErr != nil {
			err = doErr
			continue
		}
		resp.Body.Close()
		return true, elapsed, resp.StatusCode, u, nil
	}
	// Nothing answered: report the first candidate so the card still links
	// somewhere sensible rather than nowhere.
	return false, 0, 0, candidates[0], err
}

// probeCheck runs whichever check suits this entry.
func probeCheck(ctx context.Context, c NetCheck) (up bool, ms float64, code int, used string, err error) {
	if c.IsApp() {
		if c.URL == "" {
			return false, 0, 0, "", errors.New("no URL configured for this app")
		}
		return probeApp(ctx, c.URL)
	}
	up, ms, answered, err := probe(ctx, c.Address, c.Port)
	if !up {
		return false, 0, 0, c.WebURL(), err
	}
	// Reachable: now find out what it actually serves, so the card links
	// somewhere that loads. WebURL stays the fallback for a device that answers
	// TCP but no HTTP, which still deserves the operator's best guess.
	if u := deviceWebURL(ctx, c, answered); u != "" {
		return true, ms, 0, u, nil
	}
	return true, ms, 0, c.WebURL(), nil
}

// refused reports whether the peer actively rejected the connection, as opposed
// to never answering.
func refused(err error) bool {
	var se syscall.Errno
	if errors.As(err, &se) {
		return se == syscall.ECONNREFUSED || se == syscall.ECONNRESET
	}
	return false
}

// runChecks probes everything due and returns the checks whose state changed,
// so the alerter can notify on transitions only.
func (n *netChecks) runChecks(ctx context.Context, now time.Time) []NetStatus {
	n.mu.RLock()
	var due []NetCheck
	for id, c := range n.checks {
		iv := time.Duration(c.Interval) * time.Second
		if iv <= 0 {
			iv = defaultCheckInterval
		}
		st := n.state[id]
		if st == nil || now.Sub(st.Checked) >= iv {
			due = append(due, *c)
		}
	}
	n.mu.RUnlock()

	// Anything identified by MAC needs its current address before it can be
	// checked. Sweep only when at least one is unresolved: the traffic is
	// justified by looking for something, not by habit.
	needSweep := false
	for _, c := range due {
		if c.MAC != "" {
			if _, ok := n.macs.lookup(c.MAC); !ok {
				needSweep = true
				break
			}
		}
	}
	if hasMACs(due) {
		n.macs.refresh(ctx, needSweep)
	}

	// Probed concurrently: a dozen devices each waiting up to four seconds
	// would otherwise take the better part of a minute in series, and the slow
	// ones are precisely the ones in trouble.
	type result struct {
		c    NetCheck
		up   bool
		ms   float64
		code int
		used string // the URL that actually answered
		ip   string // where a MAC-identified device was found
		snmp *SNMPInfo
		e    error
	}
	results := make(chan result, len(due))
	var wg sync.WaitGroup
	for _, c := range due {
		wg.Add(1)
		go func(c NetCheck) {
			defer wg.Done()
			found := ""
			if c.MAC != "" {
				if ip, ok := n.macs.lookup(c.MAC); ok {
					c.Address, found = ip, ip
				}
			}
			up, ms, code, used, err := probeCheck(ctx, c)
			// Only when the device answered: polling something that is not there
			// just spends two seconds discovering that again.
			var snmp *SNMPInfo
			if up && c.SNMP != "" {
				snmp = snmpPoll(ctx, c.Address, c.SNMPPort, c.SNMP)
			}
			if c.MAC != "" && found == "" && err == nil {
				err = errors.New("no device with that MAC found on the hub's networks")
			}
			results <- result{c, up, ms, code, used, found, snmp, err}
		}(c)
	}
	wg.Wait()
	close(results)

	var changed []NetStatus
	n.mu.Lock()
	defer n.mu.Unlock()
	for r := range results {
		prev := n.state[r.c.ID]
		web := r.used // whichever scheme answered, so the card links to that one
		if web == "" {
			web = r.c.WebURL()
		}
		// One up-state, decided here and used for the record, the error and the
		// alert alike. A device tracked by MAC that was not found is down even
		// if the address it used to hold answers, because whatever answered is
		// somebody else.
		up := r.up && !(r.c.MAC != "" && r.ip == "")
		st := &NetStatus{NetCheck: r.c, Web: web, Up: up, LatencyMs: r.ms, Code: r.code, IP: r.ip, Checked: now}
		// A poll that failed keeps the previous reading rather than replacing it
		// with nothing: one dropped UDP packet should not blank a card.
		switch {
		case r.snmp != nil && r.snmp.Error == "":
			st.SNMP = r.snmp
		case prev != nil && prev.SNMP != nil:
			st.SNMP = prev.SNMP
			if r.snmp != nil {
				st.SNMP.Error = r.snmp.Error
			}
		default:
			st.SNMP = r.snmp
		}
		if r.e != nil && !up {
			st.Error = r.e.Error()
		}
		switch {
		case prev == nil:
			st.Since = now
			changed = append(changed, *st) // first result is worth reporting
		case prev.Up != up:
			st.Since = now
			changed = append(changed, *st)
		default:
			st.Since = prev.Since
		}
		n.state[r.c.ID] = st
	}
	return changed
}

// netCheckLoop probes devices on a tick and notifies on state changes.
func (s *Server) netCheckLoop(ctx context.Context) {
	if s.netChecks == nil {
		return
	}
	t := time.NewTicker(10 * time.Second) // the tick, not the per-check interval
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for _, st := range s.netChecks.runChecks(ctx, now) {
				// A first observation of a device that is up is not news; every
				// other transition is.
				if st.Up && st.Since.Equal(st.Checked) && st.Error == "" && s.netFirstSeen(st.ID) {
					continue
				}
				s.alerter.notifyNetCheck(st)
			}
		}
	}
}

// netFirstSeen reports whether this is the very first result for a check, so a
// newly-added device that is up does not announce itself.
func (s *Server) netFirstSeen(id string) bool {
	s.netSeenMu.Lock()
	defer s.netSeenMu.Unlock()
	if s.netSeen == nil {
		s.netSeen = map[string]bool{}
	}
	first := !s.netSeen[id]
	s.netSeen[id] = true
	return first
}

// normalizeAddress accepts what people actually paste into an address box and
// returns something dialable.
//
// The field asks for a hostname or IP, and a URL is the obvious thing to paste
// instead — at which point the dial target becomes "https://10.0.0.1:443",
// which cannot resolve and reports the device as unreachable while looking
// perfectly correct on the card. A port written into the address is likewise
// lifted out rather than left to corrupt the address.
func normalizeAddress(raw string, port int) (string, int) {
	a := strings.TrimSpace(raw)
	if a == "" {
		return "", port
	}
	if i := strings.Index(a, "://"); i >= 0 {
		if u, err := url.Parse(a); err == nil && u.Host != "" {
			a = u.Host
		} else {
			a = a[i+3:]
		}
	}
	a = strings.TrimSuffix(a, "/")
	if i := strings.IndexAny(a, "/?#"); i >= 0 {
		a = a[:i] // a path is not part of an address
	}
	// A bracketed IPv6 literal, or host:port. Bare IPv6 has many colons and no
	// brackets, and must be left exactly as it is.
	if h, p, err := net.SplitHostPort(a); err == nil {
		a = h
		if port == 0 {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
	}
	return a, port
}

// handleNetChecks lists, creates and deletes agentless checks.
func (s *Server) handleNetChecks(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.netChecks == nil {
		http.Error(w, "network checks are unavailable", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.netChecks.list())

	case http.MethodPost:
		var c NetCheck
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// The two kinds need different things: a device is an address to
		// connect to, an app is a URL to fetch. Requiring an address of both
		// would reject an app that is only ever known by its URL.
		if c.IsApp() && c.URL == "" {
			http.Error(w, "a URL is required for an app", http.StatusBadRequest)
			return
		}
		c.Address, c.Port = normalizeAddress(c.Address, c.Port)
		c.SNMP = strings.TrimSpace(c.SNMP)
		if c.MAC != "" {
			if m := normalizeMAC(c.MAC); m != "" {
				c.MAC = m
			} else {
				http.Error(w, "that MAC address is not valid", http.StatusBadRequest)
				return
			}
		}
		if !c.IsApp() && c.Address == "" && c.MAC == "" {
			http.Error(w, "an address is required for a device", http.StatusBadRequest)
			return
		}
		if c.Name == "" {
			c.Name = c.Address
			if c.Name == "" {
				c.Name = c.URL
			}
		}
		if c.ID == "" {
			c.ID = auth.RandomID(10)
		}
		s.netChecks.mu.Lock()
		s.netChecks.checks[c.ID] = &c
		delete(s.netChecks.state, c.ID) // an edited check re-probes rather than showing stale state
		s.netChecks.mu.Unlock()
		if err := s.netChecks.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("AUDIT netcheck add id=%s name=%q address=%s:%d", c.ID, c.Name, c.Address, c.Port)
		writeJSON(w, http.StatusOK, c)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		s.netChecks.mu.Lock()
		delete(s.netChecks.checks, id)
		delete(s.netChecks.state, id)
		s.netChecks.mu.Unlock()
		if err := s.netChecks.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("AUDIT netcheck remove id=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
