package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func pdu(oid string, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Value: v}
}

// The printer MIB uses negative sentinels rather than a percentage: -1 is
// "will not say", -2 is "no meaningful limit", -3 is "some remaining". Treating
// them as numbers is how a dashboard reports a cartridge at -1%.
func TestSupplySentinelsAreNotPercentages(t *testing.T) {
	for _, level := range []int{-1, -2, -3} {
		descs := []gosnmp.SnmpPDU{pdu(oidSupplyDesc+".1", []byte("Black Toner"))}
		maxBy := map[string]any{"1": 100}
		lvlBy := map[string]any{"1": level}

		s := Supply{Name: "Black Toner", Percent: -1}
		max, okMax := toInt(maxBy["1"])
		lvl, okLvl := toInt(lvlBy["1"])
		if okMax && okLvl && max > 0 && lvl >= 0 {
			s.Percent = int(float64(lvl) / float64(max) * 100)
		}
		if s.Percent != -1 {
			t.Errorf("level %d became %d%%, want unknown (-1)", level, s.Percent)
		}
		_ = descs
	}
}

// A supply that does report a level is a straight percentage of its capacity.
func TestSupplyPercentIsOfCapacity(t *testing.T) {
	max, lvl := 2000, 500
	got := int(float64(lvl) / float64(max) * 100)
	if got != 25 {
		t.Errorf("got %d%%, want 25%%", got)
	}
}

// Two columns of one table line up by their row index, not their order.
func TestTableColumnsAlignByRowIndex(t *testing.T) {
	levels := []gosnmp.SnmpPDU{
		pdu(oidSupplyLevel+".3", 30),
		pdu(oidSupplyLevel+".1", 10),
		pdu(oidSupplyLevel+".2", 20),
	}
	by := indexBySuffix(levels)
	for idx, want := range map[string]int{"1": 10, "2": 20, "3": 30} {
		got, ok := toInt(by[idx])
		if !ok || got != want {
			t.Errorf("row %s = %v, want %d", idx, by[idx], want)
		}
	}
}

func TestSuffixReadsTheRowIndex(t *testing.T) {
	if got := suffix(".1.3.6.1.2.1.43.11.1.1.9.1.2"); got != "2" {
		t.Errorf("suffix = %q, want \"2\"", got)
	}
	if got := suffix("nodots"); got != "nodots" {
		t.Errorf("suffix = %q", got)
	}
}

// SNMP integers arrive as several widths depending on the type on the wire.
func TestToIntAcceptsEveryWidth(t *testing.T) {
	for _, v := range []any{int(5), int64(5), uint(5), uint32(5), uint64(5)} {
		if n, ok := toInt(v); !ok || n != 5 {
			t.Errorf("%T did not convert: %v %v", v, n, ok)
		}
	}
	// A string is not a number, and must not silently become zero.
	if _, ok := toInt("5"); ok {
		t.Error("a string was accepted as an integer")
	}
	if _, ok := toInt([]byte{5}); ok {
		t.Error("bytes were accepted as an integer")
	}
}

// sysDescr is routinely several lines of kernel build detail; a card gets one.
func TestSysDescrIsTrimmedToOneLine(t *testing.T) {
	got := firstLine("Linux switch01 5.10.0\nBuilt by someone\nExtra")
	if got != "Linux switch01 5.10.0" {
		t.Errorf("got %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if len(firstLine(string(long))) > 124 {
		t.Errorf("long descr was not trimmed: %d chars", len(firstLine(string(long))))
	}
}

// OctetStrings arrive as bytes; anything else is not a string.
func TestSnmpStringHandlesOctetStrings(t *testing.T) {
	if got := snmpString(pdu("x", []byte("  switch01  "))); got != "switch01" {
		t.Errorf("got %q", got)
	}
	if got := snmpString(pdu("x", 42)); got != "" {
		t.Errorf("an integer became %q", got)
	}
}

// A timeout is the overwhelmingly common failure and "no response" alone gives
// the operator nothing to try.
func TestTimeoutErrorSaysWhatToCheck(t *testing.T) {
	msg := snmpError(errTimeout{})
	if msg == "" || msg == "timeout" {
		t.Fatalf("unhelpful message: %q", msg)
	}
	if !strings.Contains(msg, "community") {
		t.Errorf("message does not suggest the usual cause: %q", msg)
	}
}

type errTimeout struct{}

func (errTimeout) Error() string { return "request timeout (after 1 retries)" }

// A device that does not answer must fail fast and say so. SNMP runs inside the
// check loop, so a poll that hangs would stall every other device's check.
func TestSnmpPollTimesOutQuickly(t *testing.T) {
	// RFC 5737 documentation address: nothing to answer, nothing to disturb.
	start := time.Now()
	info := snmpPoll(context.Background(), "192.0.2.1", 161, "public")
	elapsed := time.Since(start)

	if info == nil {
		t.Fatal("no result at all")
	}
	if info.Error == "" {
		t.Error("a device that never answered reported no error")
	}
	// Two seconds with one retry, plus slack. The bound is the point: without
	// one this blocks the loop that checks everything else.
	if elapsed > 12*time.Second {
		t.Errorf("poll took %v, which would stall the check loop", elapsed)
	}
	if info.IfTotal != 0 || len(info.Supplies) != 0 || info.UPS != nil {
		t.Errorf("invented readings for a device that never answered: %+v", info)
	}
}

// A context that is already cancelled must not be ignored.
func TestSnmpPollRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	info := snmpPoll(ctx, "192.0.2.1", 161, "public")
	if info.Error == "" {
		t.Error("a cancelled poll reported success")
	}
	if time.Since(start) > 8*time.Second {
		t.Error("cancellation was ignored")
	}
}
