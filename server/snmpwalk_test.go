package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// A MAC address is an OctetString whose bytes are frequently all above 0x20, so
// a control-character test passes it through and renders six bytes of address
// as mojibake — which is exactly what a switch's ifPhysAddress column did.
func TestBinaryOctetStringsRenderAsHex(t *testing.T) {
	mac := []byte{0xd8, 0xb3, 0x70, 0xc4, 0xe1, 0x78}
	got := renderPDU(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: mac})
	if !strings.Contains(got, "d8") || !strings.Contains(got, "78") {
		t.Errorf("a MAC rendered as %q, want hex", got)
	}
	// Real text still reads as text.
	if got := renderPDU(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("Slot: 0 Port: 1")}); got != "Slot: 0 Port: 1" {
		t.Errorf("text rendered as %q", got)
	}
	// And an empty value is not text.
	if isPrintable("") {
		t.Error("an empty string was treated as printable")
	}
}

// Only digits and dots reach the OID parser.
func TestBrowseRejectsNonOIDs(t *testing.T) {
	for _, bad := range []string{"", "abc", "1.3.6;reboot", "1.3.6 1.4", strings.Repeat("1.", 100)} {
		if validOID(bad) {
			t.Errorf("accepted %q as an OID", bad)
		}
	}
	for _, good := range []string{"1.3.6.1.2.1.1", ".1.3.6.1.4.1.41112"} {
		if !validOID(good) {
			t.Errorf("rejected %q", good)
		}
	}
}

// A vendor subtree is mostly tables, and a table is one column repeated per
// port. Listing every value buries the shape; listing columns shows where the
// interesting numbers are, which is the whole task when hunting an
// undocumented reading.
func TestSummariseCollapsesTableColumns(t *testing.T) {
	var rows []walkRow
	for i := 1; i <= 26; i++ {
		rows = append(rows,
			walkRow{OID: "1.3.6.1.4.1.4413.1.1.43.1.8.1.5." + itoa(i), Type: "gauge", Value: itoa(i * 100)},
			walkRow{OID: "1.3.6.1.4.1.4413.1.1.43.1.8.1.6." + itoa(i), Type: "int", Value: "1"})
	}
	rows = append(rows, walkRow{OID: "1.3.6.1.4.1.4413.1.1.43.1.1.0", Type: "int", Value: "250"})

	got := summarise(rows)
	if len(got) != 3 {
		t.Fatalf("collapsed to %d columns, want 3: %+v", len(got), got)
	}
	if got[0].Count != 26 || len(got[0].Samples) != 4 {
		t.Errorf("first column: count=%d samples=%v", got[0].Count, got[0].Samples)
	}
	// A scalar ends in .0 and is one value, not a column to strip an index off.
	if got[2].OID != "1.3.6.1.4.1.4413.1.1.43.1.1.0" || got[2].Count != 1 {
		t.Errorf("scalar was collapsed: %+v", got[2])
	}
}

func TestColumnOfKeepsScalars(t *testing.T) {
	if got := columnOf("1.3.6.1.2.1.1.5.0"); got != "1.3.6.1.2.1.1.5.0" {
		t.Errorf("scalar became %q", got)
	}
	if got := columnOf("1.3.6.1.2.1.2.2.1.8.24"); got != "1.3.6.1.2.1.2.2.1.8" {
		t.Errorf("column became %q", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// An empty walk has two very different causes, and the message has to say
// which: a device that is not answering at all sends people hunting for OIDs
// that were never the problem.
func TestUnreachableIsNotReportedAsUnimplemented(t *testing.T) {
	// Nothing listens on a documentation address, so the reachability probe
	// must fail rather than report the device as present-but-sparse.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := snmpReachable(ctx, "192.0.2.1", 161, SNMPCreds{Community: "public", Version: "2c"})
	if err == nil {
		t.Fatal("a device that never answered was reported reachable")
	}
	msg := snmpError(err)
	if !strings.Contains(msg, "community") && !strings.Contains(msg, "timeout") {
		t.Errorf("unhelpful message: %q", msg)
	}
}
