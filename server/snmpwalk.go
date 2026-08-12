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
		ID  string `json:"id"`
		OID string `json:"oid"`
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

	rows, err := snmpBrowse(ctx, addr, c.SNMPPort, snmpCreds(c), root)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": snmpError(err), "rows": []walkRow{}})
		return
	}
	s.audit(r, "snmp:browse", req.ID, root, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "root": root})
}

// snmpBrowse walks a subtree and renders what came back.
func snmpBrowse(ctx context.Context, host string, port int, creds SNMPCreds, root string) ([]walkRow, error) {
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
		if len(rows) >= maxBrowseRows {
			return fmt.Errorf("stopping at %d rows", maxBrowseRows)
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
// hex — a MAC address in an OctetString is bytes, not a word.
func isPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' || r == 0x7f {
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
