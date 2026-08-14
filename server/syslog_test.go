package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Devices emit every dialect of syslog ever half-specified, and a parser that
// rejects a malformed line throws away exactly the message a failing device
// managed to get out.
func TestSyslogDialects(t *testing.T) {
	for _, c := range []struct {
		in       string
		sev, fac int
		tag      string
		text     string
	}{
		// RFC 3164, the dialect most appliances actually speak.
		{"<27>Aug 13 21:04:11 pfsense kernel: em0: link state changed to DOWN",
			3, 3, "kernel", "em0: link state changed to DOWN"},
		// A tag with a pid.
		{"<85>Aug  3 09:00:01 nas sshd[2211]: Failed password for root",
			5, 10, "sshd", "Failed password for root"},
		// RFC 5424.
		{`<165>1 2026-08-13T21:04:11Z sw01 lldpd 812 ID47 - neighbor added on port 14`,
			5, 20, "lldpd", "neighbor added on port 14"},
		// 5424 with bracketed structured data, which splits on spaces.
		{`<165>1 2026-08-13T21:04:11Z sw01 app 1 ID [x@1 k="v v"] the actual message`,
			5, 20, "app", "the actual message"},
		// No priority at all: everything lands in the text rather than nowhere.
		{"something cried for help", 6, 1, "", "something cried for help"},
		// A priority and nothing else parseable.
		{"<14>watchdog reset", 6, 1, "", "watchdog reset"},
	} {
		m := parseSyslog(c.in)
		if m.Severity != c.sev || m.Facility != c.fac {
			t.Errorf("%q: sev/fac = %d/%d, want %d/%d", c.in, m.Severity, m.Facility, c.sev, c.fac)
		}
		if m.Tag != c.tag {
			t.Errorf("%q: tag = %q, want %q", c.in, m.Tag, c.tag)
		}
		if m.Text != c.text {
			t.Errorf("%q: text = %q, want %q", c.in, m.Text, c.text)
		}
	}
}

// The store is a bounded ring: the newest survive, the oldest go, and nothing
// grows without limit — an unbounded log store on a small disk is a way to
// lose the hub.
func TestSyslogStoreIsBoundedAndNewestFirst(t *testing.T) {
	st := newSyslogStore()
	now := time.Now().Unix()
	for i := 0; i < syslogKeep+500; i++ {
		st.add(SyslogMsg{Received: now, Source: "10.0.0.1", Text: fmt.Sprintf("msg %d", i)})
	}
	got := st.query("", "", 3)
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Text != fmt.Sprintf("msg %d", syslogKeep+499) {
		t.Errorf("newest = %q", got[0].Text)
	}
	// The overwritten oldest are genuinely gone.
	if hits := st.query("", "msg 100 ", 10); len(hits) != 0 {
		t.Errorf("an overwritten message survived: %+v", hits)
	}
}

func TestSyslogQueryFilters(t *testing.T) {
	st := newSyslogStore()
	now := time.Now().Unix()
	st.add(SyslogMsg{Received: now, Source: "10.0.0.1", Tag: "sshd", Text: "Failed password"})
	st.add(SyslogMsg{Received: now, Source: "10.0.0.2", Tag: "kernel", Text: "link DOWN"})
	st.add(SyslogMsg{Received: now - int64(syslogMaxAge/time.Second) - 60, Source: "10.0.0.3", Text: "ancient"})

	if got := st.query("10.0.0.2", "", 10); len(got) != 1 || got[0].Tag != "kernel" {
		t.Errorf("source filter: %+v", got)
	}
	// Case-insensitive, and the tag is searchable text too.
	if got := st.query("", "SSHD", 10); len(got) != 1 {
		t.Errorf("text filter: %+v", got)
	}
	for _, m := range st.query("", "", 10) {
		if m.Text == "ancient" {
			t.Error("a message older than retention was returned")
		}
	}
	srcs := st.sources()
	if len(srcs) != 3 {
		t.Errorf("sources = %v", srcs)
	}
}

// End to end over real UDP: a datagram in, the API answering with it.
func TestSyslogEndToEnd(t *testing.T) {
	s := &Server{cfg: Config{AdminToken: testAdminToken}, syslog: newSyslogStore()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Port 0: the kernel picks, the test reads it back.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	go s.runSyslog(ctx, addr)
	time.Sleep(50 * time.Millisecond)

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("<27>Aug 13 21:04:11 pfsense kernel: em0: link state changed to DOWN"))
	conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(s.syslog.query("", "", 10)) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/syslog?q=link", nil)
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	w := httptest.NewRecorder()
	s.handleSyslog(w, r)
	var resp struct {
		Enabled  bool        `json:"enabled"`
		Messages []SyslogMsg `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Enabled || len(resp.Messages) != 1 {
		t.Fatalf("resp = %s", w.Body.String())
	}
	m := resp.Messages[0]
	if m.Severity != 3 || m.Tag != "kernel" || !strings.HasPrefix(m.Source, "127.0.0.1") {
		t.Errorf("message = %+v", m)
	}
	if m.Received == 0 {
		t.Error("no received time — device clocks are wrong too often to rely on the claimed one")
	}
}

// A hub with no listener configured says so, rather than answering with an
// empty list that looks like a quiet network.
func TestSyslogSaysWhenItIsOff(t *testing.T) {
	s := &Server{cfg: Config{AdminToken: testAdminToken}}
	r := httptest.NewRequest(http.MethodGet, "/api/syslog", nil)
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	w := httptest.NewRecorder()
	s.handleSyslog(w, r)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("body = %s", w.Body.String())
	}
}
