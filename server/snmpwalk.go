package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Browsing what a device actually exposes.
//
// Every device class needs its own OIDs, and vendor documentation is often
// wrong, absent, or describes a different firmware. Guessing from here and
// waiting to hear whether it worked is a slow way to find out; walking the
// device and reading the answer is a fast one.
//
// Read-only, one subtree at a time, bounded. This is a diagnostic, not a
// management interface.

// walkRow is one value a device returned.
type walkRow struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// maxBrowseRows bounds a browse. Deep enough to see a whole vendor subtree,
// short enough that pointing this at the root of a switch's routing table does
// not hold a request open for a minute.
const maxBrowseRows = 400

func (s *Server) handleSNMPWalk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		ID      string `json:"id"`
		OID     string `json:"oid"`
		Summary bool   `json:"summary"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	c, ok := s.netChecks.byID(req.ID)
	if !ok {
		http.Error(w, "no such device", http.StatusNotFound)
		return
	}
	if !snmpConfigured(c) {
		http.Error(w, "SNMP is not configured on this device", http.StatusConflict)
		return
	}
	// Whatever address it is actually at, which for a MAC-tracked device is not
	// the one that was typed in.
	addr := c.Address
	if st, ok := s.netChecks.statusByID(req.ID); ok && st.IP != "" {
		addr = st.IP
	}

	root := strings.TrimSpace(req.OID)
	if root == "" {
		root = "1.3.6.1.2.1.1" // the system group: always present, always small
	}
	if !validOID(root) {
		http.Error(w, "that does not look like an OID", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// A vendor subtree runs to thousands of values, and the row cap turns that
	// into the first four hundred — which is a table of interface indices and
	// no help at all in finding where the interesting numbers live. Summarising
	// walks further and reports columns rather than values.
	limit := maxBrowseRows
	if req.Summary {
		limit = maxSummaryRows
	}
	rows, err := snmpBrowse(ctx, addr, c.SNMPPort, snmpCreds(c), root, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": snmpError(err), "rows": []walkRow{}})
		return
	}
	s.audit(r, "snmp:browse", req.ID, root, "ok")
	// An empty walk has two very different causes, and saying "this device does
	// not implement it" for both sends people hunting for OIDs on a device that
	// is not answering at all. The system group is mandatory for any agent, so
	// asking for it settles which one this is.
	if len(rows) == 0 {
		if err := snmpReachable(ctx, addr, c.SNMPPort, snmpCreds(c)); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"error": "this device is not answering SNMP at all — " + snmpError(err),
				"rows":  []walkRow{},
			})
			return
		}
	}

	out := map[string]any{"root": root, "truncated": len(rows) >= limit}
	if req.Summary {
		out["groups"] = summarise(rows)
		out["count"] = len(rows)
	} else {
		out["rows"] = rows
	}
	writeJSON(w, http.StatusOK, out)
}

// snmpReachable asks for the one object every SNMP agent must implement.
//
// If this fails, nothing else about the device's OIDs matters: the credentials
// are wrong, the port is wrong, or SNMP is not running. If it succeeds, the
// agent is there and the subtree genuinely is not implemented.
func snmpReachable(ctx context.Context, host string, port int, creds SNMPCreds) error {
	if port <= 0 {
		port = 161
	}
	version := gosnmp.Version2c
	switch creds.Version {
	case "1":
		version = gosnmp.Version1
	case "3":
		version = gosnmp.Version3
	}
	g := &gosnmp.GoSNMP{
		Target: host, Port: uint16(port), Community: creds.Community,
		Version: version, Timeout: 3 * time.Second, Retries: 1,
		MaxOids: gosnmp.MaxOids, Context: ctx,
	}
	if version == gosnmp.Version3 {
		applyV3(g, creds)
	}
	if err := g.Connect(); err != nil {
		return err
	}
	defer g.Conn.Close()

	res, err := g.Get([]string{oidSysDescr})
	if err != nil {
		return err
	}
	// An agent that answers with "no such object" for sysDescr is not an agent
	// this can work with, and reporting it as reachable would be misleading.
	for _, v := range res.Variables {
		if v.Type == gosnmp.NoSuchObject || v.Type == gosnmp.NoSuchInstance {
			return fmt.Errorf("the device answered but has no system description")
		}
	}
	return nil
}

// snmpBrowse walks a subtree and renders what came back.
func snmpBrowse(ctx context.Context, host string, port int, creds SNMPCreds, root string, limit int) ([]walkRow, error) {
	version := gosnmp.Version2c
	switch creds.Version {
	case "1":
		version = gosnmp.Version1
	case "3":
		version = gosnmp.Version3
	}
	if port <= 0 {
		port = 161
	}
	g := &gosnmp.GoSNMP{
		Target: host, Port: uint16(port), Community: creds.Community,
		Version: version, Timeout: 3 * time.Second, Retries: 1,
		MaxOids: gosnmp.MaxOids, Context: ctx,
	}
	if version == gosnmp.Version3 {
		applyV3(g, creds)
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	defer g.Conn.Close()

	walker := g.BulkWalk
	if version == gosnmp.Version1 {
		walker = g.Walk
	}
	var rows []walkRow
	err := walker(root, func(p gosnmp.SnmpPDU) error {
		if len(rows) >= limit {
			return fmt.Errorf("stopping at %d rows", limit)
		}
		rows = append(rows, walkRow{
			OID:   strings.TrimPrefix(p.Name, "."),
			Type:  pduTypeName(p.Type),
			Value: renderPDU(p),
		})
		return nil
	})
	// A partial answer is still an answer: hitting the row cap, or a device
	// that stops responding halfway, should show what it did say.
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	return rows, nil
}

// maxSummaryRows is how far a summarising walk goes. Higher than a listing
// walk because the answer is a few dozen lines however many values it read.
const maxSummaryRows = 6000

// walkGroup is one column of a table: every row under it shares an OID prefix.
type walkGroup struct {
	OID     string   `json:"oid"`
	Count   int      `json:"count"`
	Type    string   `json:"type"`
	Samples []string `json:"samples"`
}

// summarise collapses a walk into its columns.
//
// A vendor subtree is mostly tables, and a table is one column repeated per
// port or per outlet. Listing every value buries the shape; listing the columns
// with a few sample values shows where the interesting numbers are, which is
// the whole task when hunting for an undocumented reading.
func summarise(rows []walkRow) []walkGroup {
	order := []string{}
	byCol := map[string]*walkGroup{}
	for _, r := range rows {
		col := columnOf(r.OID)
		g, ok := byCol[col]
		if !ok {
			g = &walkGroup{OID: col, Type: r.Type}
			byCol[col] = g
			order = append(order, col)
		}
		g.Count++
		if len(g.Samples) < 4 && r.Value != "" {
			g.Samples = append(g.Samples, r.Value)
		}
	}
	out := make([]walkGroup, 0, len(order))
	for _, col := range order {
		out = append(out, *byCol[col])
	}
	return out
}

// columnOf strips a row index off an OID, so every entry in one table column
// collapses to the same key. A scalar ends in .0 and is left as it is, since it
// is already one value rather than a column.
func columnOf(oid string) string {
	i := strings.LastIndexByte(oid, '.')
	if i < 0 {
		return oid
	}
	if oid[i+1:] == "0" {
		return oid
	}
	return oid[:i]
}

// renderPDU turns a value into something readable, whatever type it arrived as.
func renderPDU(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case []byte:
		s := strings.TrimSpace(string(v))
		if isPrintable(s) {
			return s
		}
		return fmt.Sprintf("% x", v) // hex, for MACs and other binary
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// isPrintable reports whether a string is worth showing as text rather than as
// hex.
//
// Printable ASCII only. A MAC address is an OctetString whose bytes are often
// all above 0x20, so a control-character test passes it through and renders six
// bytes of address as mojibake — which is what a switch's ifPhysAddress column
// looked like.
func isPrintable(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func pduTypeName(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.OctetString:
		return "string"
	case gosnmp.Integer:
		return "int"
	case gosnmp.Counter32, gosnmp.Counter64:
		return "counter"
	case gosnmp.Gauge32:
		return "gauge"
	case gosnmp.TimeTicks:
		return "ticks"
	case gosnmp.ObjectIdentifier:
		return "oid"
	case gosnmp.IPAddress:
		return "ip"
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
		return "absent"
	}
	return "?"
}

// validOID keeps anything that is not digits and dots out of a string that goes
// on to be parsed as an object identifier.
func validOID(s string) bool {
	s = strings.TrimPrefix(s, ".")
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
