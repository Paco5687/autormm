package server

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// host is the server-side record for one monitored machine.
type host struct {
	reg      protocol.Register
	lastSeen time.Time
	online   bool
	metrics  *protocol.Metrics
	cpuHist  []float64
	memHist  []float64
	conn     *agentConn // interactive control connection (tray/user session); nil when offline
	elevConn *agentConn // optional privileged (SYSTEM/root) helper connection
}

// Store keeps the live registry of hosts.
type Store struct {
	mu           sync.RWMutex
	hosts        map[string]*host
	historyLen   int
	offlineAfter time.Duration
	history      *History // persisted samples; may be nil
}

// NewStore creates an empty store. history may be nil to disable persistence.
func NewStore(historyLen int, offlineAfter time.Duration, history *History) *Store {
	return &Store{
		hosts:        map[string]*host{},
		historyLen:   historyLen,
		offlineAfter: offlineAfter,
		history:      history,
	}
}

// register records (or refreshes) a host and attaches its control connection.
// Any previous connection for the same agent id is returned so the caller can
// close it.
func (s *Store) register(reg protocol.Register, conn *agentConn) (old *agentConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hosts[reg.AgentID]
	if h == nil {
		h = &host{}
		s.hosts[reg.AgentID] = h
	}
	h.lastSeen = time.Now()
	if reg.Elevated {
		old = h.elevConn
		h.elevConn = conn
		if h.conn == nil {
			h.reg = reg // no interactive agent yet — adopt this identity
		}
	} else {
		old = h.conn
		h.conn = conn
		h.reg = reg
	}
	h.online = h.conn != nil || h.elevConn != nil
	return old
}

// disconnect marks a host offline if the given connection is still the current one.
func (s *Store) disconnect(agentID string, conn *agentConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hosts[agentID]
	if h == nil {
		return
	}
	if h.conn == conn {
		h.conn = nil
	}
	if h.elevConn == conn {
		h.elevConn = nil
	}
	h.online = h.conn != nil || h.elevConn != nil
}

// updateMetrics stores the latest snapshot and appends to history ring buffers.
func (s *Store) updateMetrics(agentID string, m *protocol.Metrics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hosts[agentID]
	if h == nil {
		return
	}
	h.metrics = m
	h.lastSeen = time.Now()
	h.cpuHist = ring(h.cpuHist, m.CPUPercent, s.historyLen)
	h.memHist = ring(h.memHist, m.MemPercent, s.historyLen)
	s.history.Insert(agentID, m) // no-op when history is nil
}

// connFor returns the live control connection for a host, or nil.
func (s *Store) connFor(agentID string) *agentConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if h := s.hosts[agentID]; h != nil {
		return h.conn
	}
	return nil
}

// onlineConns returns the control connections of all currently-online hosts.
func (s *Store) onlineConns() []*agentConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var conns []*agentConn
	for _, h := range s.hosts {
		if h.conn != nil {
			conns = append(conns, h.conn)
		}
		if h.elevConn != nil {
			conns = append(conns, h.elevConn)
		}
	}
	return conns
}

// canStream reports whether a host is online and supports screen capture.
func (s *Store) canStream(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hosts[agentID]
	return h != nil && h.online && h.reg.CanStream
}

// canExec reports whether a host can run commands (via its elevated helper if
// present, else the interactive agent).
func (s *Store) canExec(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hosts[agentID]
	if h == nil {
		return false
	}
	return h.elevConn != nil || (h.online && h.reg.CanExec)
}

// execConn returns the connection to run commands on — the privileged helper
// when attached (so exec/service/patch run as SYSTEM/root), else the interactive
// agent.
func (s *Store) execConn(agentID string) *agentConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hosts[agentID]
	if h == nil {
		return nil
	}
	if h.elevConn != nil {
		return h.elevConn
	}
	return h.conn
}

// hasElevated reports whether a privileged helper is attached to the host.
func (s *Store) hasElevated(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hosts[agentID]
	return h != nil && h.elevConn != nil
}

// osFor returns a host's reported OS ("linux", "windows", …), or "" if unknown.
func (s *Store) osFor(agentID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if h := s.hosts[agentID]; h != nil {
		return h.reg.OS
	}
	return ""
}

// encoderCaps returns the video codecs a host can produce.
func (s *Store) encoderCaps(agentID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if h := s.hosts[agentID]; h != nil {
		return h.reg.EncoderCaps
	}
	return nil
}

// views returns a stable, sorted snapshot for the API.
func (s *Store) views() []protocol.HostView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.HostView, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, s.viewLocked(h))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online // online first
		}
		return out[i].Hostname < out[j].Hostname
	})
	return out
}

func (s *Store) viewLocked(h *host) protocol.HostView {
	v := protocol.HostView{
		AgentID:      h.reg.AgentID,
		Hostname:     h.reg.Hostname,
		OS:           h.reg.OS,
		Platform:     h.reg.Platform,
		Arch:         h.reg.Arch,
		AgentVersion: h.reg.AgentVersion,
		CanStream:    h.reg.CanStream,
		CanExec:      h.reg.CanExec || h.elevConn != nil,
		Elevated:     h.elevConn != nil,
		Facts:        h.reg.Facts,
		Tags:         h.reg.Tags,
		Online:       h.online,
		LastSeen:     h.lastSeen,
		Metrics:      h.metrics,
		CPUHistory:   append([]float64(nil), h.cpuHist...),
		MemHistory:   append([]float64(nil), h.memHist...),
	}
	v.Alerts = computeAlerts(h, s.offlineAfter)
	return v
}

// computeAlerts derives simple threshold warnings for the dashboard.
func computeAlerts(h *host, offlineAfter time.Duration) []string {
	var a []string
	if !h.online {
		a = append(a, "offline")
		return a
	}
	if time.Since(h.lastSeen) > offlineAfter {
		a = append(a, "stale (no recent telemetry)")
	}
	m := h.metrics
	if m == nil {
		return a
	}
	if m.CPUPercent >= 90 {
		a = append(a, "high CPU")
	}
	if m.MemPercent >= 90 {
		a = append(a, "high memory")
	}
	for _, d := range m.Disks {
		if d.Percent >= 90 {
			a = append(a, "disk "+d.Mount+" almost full")
		}
	}
	return a
}

// reaper periodically wakes so time-based alerts refresh even without new data.
func (s *Store) reaper(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Nothing to mutate here; online flips happen on disconnect. This
			// ticker exists so future periodic maintenance has a home.
		}
	}
}

func ring(buf []float64, v float64, max int) []float64 {
	buf = append(buf, v)
	if len(buf) > max {
		buf = buf[len(buf)-max:]
	}
	return buf
}
