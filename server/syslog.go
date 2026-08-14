package server

import (
	"context"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A syslog collector.
//
// A firewall, a switch and a NAS all have something to say and nowhere to say
// it — and the devices that most need a listener are exactly the ones that
// cannot run an agent, which is the same set the device checks exist for. The
// hub is the one thing on the network already watching all of them, and all
// they need is one line of configuration pointing at it.
//
// Off unless configured. Syslog over UDP is unauthenticated and trivially
// spoofed, so a message is evidence of what a device said, not proof of who
// said it — and the listener belongs on the LAN.

// SyslogMsg is one received message.
type SyslogMsg struct {
	// Received is the hub's clock; Claimed is the device's. Both are kept
	// because device clocks are wrong often enough to matter, and the
	// difference between the two is itself a finding.
	Received int64  `json:"received"`
	Claimed  int64  `json:"claimed,omitempty"`
	Source   string `json:"source"`
	Severity int    `json:"severity"`
	Facility int    `json:"facility"`
	Tag      string `json:"tag,omitempty"`
	Text     string `json:"text"`
}

const (
	// syslogKeep bounds the store by count and the pruner by age. An unbounded
	// log store on a hub with a small disk is a way to lose the hub.
	syslogKeep   = 20000
	syslogMaxAge = 7 * 24 * time.Hour
	// syslogMaxMsg truncates a datagram: a syslog line is a line, and a device
	// sending kilobytes per message is misbehaving in a way truncation records
	// perfectly well.
	syslogMaxMsg = 2048
)

// syslogStore is a bounded in-memory ring.
//
// Memory rather than the history database, deliberately: logs are the noisiest
// data the hub handles and the least valuable per byte, and a week of a chatty
// switch should never compete with metrics history for the disk. Twenty
// thousand messages of a few hundred bytes is a few megabytes, and a hub
// restart losing recent logs is acceptable in a way losing metrics is not.
type syslogStore struct {
	mu   sync.Mutex
	ring []SyslogMsg
	next int
	full bool
}

func newSyslogStore() *syslogStore {
	return &syslogStore{ring: make([]SyslogMsg, syslogKeep)}
}

func (st *syslogStore) add(m SyslogMsg) {
	st.mu.Lock()
	st.ring[st.next] = m
	st.next++
	if st.next == len(st.ring) {
		st.next, st.full = 0, true
	}
	st.mu.Unlock()
}

// query returns the newest messages first, filtered by source and substring.
func (st *syslogStore) query(source, contains string, limit int) []SyslogMsg {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	cutoff := time.Now().Add(-syslogMaxAge).Unix()
	contains = strings.ToLower(contains)
	st.mu.Lock()
	defer st.mu.Unlock()
	n := st.next
	if st.full {
		n = len(st.ring)
	}
	out := make([]SyslogMsg, 0, limit)
	// Walk backwards from the write position: newest first without a sort.
	for i := 0; i < n && len(out) < limit; i++ {
		idx := st.next - 1 - i
		if idx < 0 {
			idx += len(st.ring)
		}
		m := st.ring[idx]
		if m.Received < cutoff {
			// Skip rather than stop: receive order is almost time order, but
			// "almost" is not a thing to build an early exit on, and a full
			// scan of the ring is twenty thousand comparisons at worst.
			continue
		}
		if source != "" && m.Source != source {
			continue
		}
		if contains != "" && !strings.Contains(strings.ToLower(m.Text+" "+m.Tag), contains) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// sources lists where messages have come from, for the viewer's filter.
func (st *syslogStore) sources() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	n := st.next
	if st.full {
		n = len(st.ring)
	}
	for i := 0; i < n; i++ {
		if s := st.ring[i].Source; s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// runSyslog listens until the context ends.
func (s *Server) runSyslog(ctx context.Context, addr string) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Printf("syslog: cannot listen on %s: %v", addr, err)
		return
	}
	log.Printf("syslog: listening on %s (udp)", addr)
	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	buf := make([]byte, 64<<10)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return // closed
		}
		msg := parseSyslog(string(buf[:min(n, syslogMaxMsg)]))
		if host, _, err := net.SplitHostPort(from.String()); err == nil {
			msg.Source = host
		} else {
			msg.Source = from.String()
		}
		msg.Received = time.Now().Unix()
		s.syslog.add(msg)
	}
}

// rfc3164TS matches the classic timestamp: "Jan  2 15:04:05".
var rfc3164TS = regexp.MustCompile(`^[A-Z][a-z]{2} [ \d]\d \d\d:\d\d:\d\d `)

// parseSyslog reads what it can and keeps the rest verbatim.
//
// Loose on purpose. Devices emit every dialect of syslog ever half-specified,
// and a parser that rejects a malformed line throws away exactly the message a
// failing device managed to get out. Anything unparsed lands in the text.
func parseSyslog(raw string) SyslogMsg {
	m := SyslogMsg{Severity: 6, Facility: 1} // info/user, the conventional default
	rest := strings.TrimSpace(raw)

	// <PRI>
	if strings.HasPrefix(rest, "<") {
		if i := strings.IndexByte(rest, '>'); i > 1 && i <= 4 {
			if pri, err := strconv.Atoi(rest[1:i]); err == nil && pri >= 0 && pri <= 191 {
				m.Facility, m.Severity = pri/8, pri%8
				rest = rest[i+1:]
			}
		}
	}

	// RFC 5424: VERSION TIME HOST APP PROCID MSGID SD MSG. The structured-data
	// field is "-" or bracketed; either way the message is what comes after it,
	// and the app name is the only header field worth keeping — the host is
	// implied by the source address.
	if strings.HasPrefix(rest, "1 ") {
		fields := strings.SplitN(rest[2:], " ", 7)
		if len(fields) >= 6 {
			if ts, err := time.Parse(time.RFC3339, fields[0]); err == nil {
				m.Claimed = ts.Unix()
				m.Tag = fields[2]
				text := ""
				if len(fields) == 7 {
					text = fields[6]
				}
				if sd := fields[5]; sd != "-" && strings.HasPrefix(sd, "[") {
					// Bracketed structured data may have been split mid-way;
					// take everything after its closing bracket.
					joined := strings.Join(fields[5:], " ")
					if i := strings.LastIndex(joined, "] "); i >= 0 {
						text = joined[i+2:]
					}
				}
				m.Text = strings.TrimSpace(text)
				return m
			}
		}
	}

	// RFC 3164: TIMESTAMP HOST TAG: MSG
	if loc := rfc3164TS.FindString(rest); loc != "" {
		if ts, err := time.Parse(time.Stamp, strings.TrimSpace(loc)); err == nil {
			// The classic format has no year; the only sane guess is this one.
			now := time.Now()
			ts = ts.AddDate(now.Year(), 0, 0)
			if ts.After(now.Add(48 * time.Hour)) {
				ts = ts.AddDate(-1, 0, 0) // a December message read in January
			}
			m.Claimed = ts.Unix()
		}
		rest = rest[len(loc):]
		// HOST TAG: MSG — the tag is the first token ending in a colon within
		// the first two tokens.
		parts := strings.SplitN(rest, " ", 3)
		if len(parts) >= 2 {
			for i := 0; i < 2 && i < len(parts); i++ {
				if t := strings.TrimSuffix(parts[i], ":"); t != parts[i] {
					m.Tag = strings.TrimSuffix(strings.Split(t, "[")[0], "]")
					rest = strings.TrimSpace(strings.Join(parts[i+1:], " "))
					break
				}
			}
		}
	}
	m.Text = strings.TrimSpace(rest)
	return m
}

func (s *Server) handleSyslog(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.syslog == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"messages": s.syslog.query(q.Get("source"), q.Get("q"), limit),
		"sources":  s.syslog.sources(),
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
