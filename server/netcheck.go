package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
}

// NetStatus is the last observed state of a check.
type NetStatus struct {
	NetCheck
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
}

func newNetChecks(dir string) *netChecks {
	n := &netChecks{checks: map[string]*NetCheck{}, state: map[string]*NetStatus{}}
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
			out = append(out, *st)
			continue
		}
		out = append(out, NetStatus{NetCheck: *c}) // not yet probed
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
func probe(ctx context.Context, address string, port int) (up bool, ms float64, err error) {
	ports := []int{port}
	if port == 0 {
		ports = commonPorts
	}
	for _, p := range ports {
		start := time.Now()
		d := net.Dialer{Timeout: checkTimeout}
		conn, dialErr := d.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(p)))
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if dialErr == nil {
			conn.Close()
			return true, elapsed, nil
		}
		err = dialErr
		if refused(dialErr) {
			return true, elapsed, nil
		}
	}
	return false, 0, err
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

	// Probed concurrently: a dozen devices each waiting up to four seconds
	// would otherwise take the better part of a minute in series, and the slow
	// ones are precisely the ones in trouble.
	type result struct {
		c  NetCheck
		up bool
		ms float64
		e  error
	}
	results := make(chan result, len(due))
	var wg sync.WaitGroup
	for _, c := range due {
		wg.Add(1)
		go func(c NetCheck) {
			defer wg.Done()
			up, ms, err := probe(ctx, c.Address, c.Port)
			results <- result{c, up, ms, err}
		}(c)
	}
	wg.Wait()
	close(results)

	var changed []NetStatus
	n.mu.Lock()
	defer n.mu.Unlock()
	for r := range results {
		prev := n.state[r.c.ID]
		st := &NetStatus{NetCheck: r.c, Up: r.up, LatencyMs: r.ms, Checked: now}
		if r.e != nil && !r.up {
			st.Error = r.e.Error()
		}
		switch {
		case prev == nil:
			st.Since = now
			changed = append(changed, *st) // first result is worth reporting
		case prev.Up != r.up:
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
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.Address == "" {
			http.Error(w, "address is required", http.StatusBadRequest)
			return
		}
		if c.Name == "" {
			c.Name = c.Address
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
