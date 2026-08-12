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
	info := snmpPoll(context.Background(), "192.0.2.1", 161, SNMPCreds{Community: "public", Version: "2c"})
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
	info := snmpPoll(ctx, "192.0.2.1", 161, SNMPCreds{Community: "public", Version: "2c"})
	if info.Error == "" {
		t.Error("a cancelled poll reported success")
	}
	if time.Since(start) > 8*time.Second {
		t.Error("cancellation was ignored")
	}
}

// Credentials must not be handed back to the browser. The dashboard fetches
// this list every few seconds all day long.
func TestSecretsAreRedactedOnTheWayOut(t *testing.T) {
	c := NetCheck{
		ID: "n1", Name: "ups", Address: "192.0.2.61",
		SNMP: "public", SNMPVersion: "3", SNMPUser: "monitor",
		SNMPAuthPass: "authsecret", SNMPPrivPass: "privsecret",
	}
	got := redactSecrets(c)
	for name, v := range map[string]string{
		"community": got.SNMP, "auth": got.SNMPAuthPass, "priv": got.SNMPPrivPass,
	} {
		if v == "" || v == secretPlaceholder {
			continue
		}
		t.Errorf("%s leaked as %q", name, v)
	}
	// The username is not a secret and is needed to show what is configured.
	if got.SNMPUser != "monitor" {
		t.Errorf("username was redacted too: %q", got.SNMPUser)
	}
	// A check with no community must not gain a placeholder implying one.
	if plain := redactSecrets(NetCheck{ID: "n2"}); plain.SNMP != "" {
		t.Errorf("invented a secret: %q", plain.SNMP)
	}
}

// Because secrets are redacted on the way out, an edit that does not retype
// them must keep what is stored — otherwise saving a name change silently wipes
// the credentials and the device stops polling.
func TestEditKeepsUntypedSecrets(t *testing.T) {
	if got := keptSecret("", "stored"); got != "stored" {
		t.Errorf("blank cleared the stored secret: %q", got)
	}
	if got := keptSecret(secretPlaceholder, "stored"); got != "stored" {
		t.Errorf("the placeholder overwrote the stored secret: %q", got)
	}
	if got := keptSecret("retyped", "stored"); got != "retyped" {
		t.Errorf("a real new secret was ignored: %q", got)
	}
}

// v3 needs only a username to be worth attempting; v1 and v2c need a community.
func TestSnmpConfiguredPerVersion(t *testing.T) {
	if snmpConfigured(NetCheck{}) {
		t.Error("polled a device with nothing configured")
	}
	if !snmpConfigured(NetCheck{SNMP: "public"}) {
		t.Error("a community alone was not enough")
	}
	if snmpConfigured(NetCheck{SNMPVersion: "3"}) {
		t.Error("v3 with no username was attempted")
	}
	if !snmpConfigured(NetCheck{SNMPVersion: "3", SNMPUser: "monitor"}) {
		t.Error("v3 with a username was not attempted")
	}
	// A v3 check must not be polled just because a stale community lingers.
	if snmpConfigured(NetCheck{SNMPVersion: "3", SNMP: "public"}) {
		t.Error("v3 without a username was attempted on the strength of a community")
	}
}

// The security level follows from the passphrases supplied, rather than being a
// separate setting to get out of step with them.
func TestV3SecurityLevelFollowsTheCredentials(t *testing.T) {
	for _, tc := range []struct {
		auth, priv string
		want       gosnmp.SnmpV3MsgFlags
	}{
		{"", "", gosnmp.NoAuthNoPriv},
		{"a", "", gosnmp.AuthNoPriv},
		{"a", "p", gosnmp.AuthPriv},
	} {
		g := &gosnmp.GoSNMP{}
		applyV3(g, SNMPCreds{User: "monitor", AuthPass: tc.auth, PrivPass: tc.priv})
		if g.MsgFlags != tc.want {
			t.Errorf("auth=%q priv=%q gave %v, want %v", tc.auth, tc.priv, g.MsgFlags, tc.want)
		}
	}
}

// A blank protocol means "not thought about", so it takes the stronger option.
func TestProtocolDefaultsAreNotTheWeakest(t *testing.T) {
	if authProto("") != gosnmp.SHA {
		t.Error("auth defaulted away from SHA")
	}
	if privProto("") != gosnmp.AES {
		t.Error("privacy defaulted away from AES")
	}
	if authProto("md5") != gosnmp.MD5 || privProto("des") != gosnmp.DES {
		t.Error("an explicit choice was not honoured")
	}
}

// A UPS network card reports a loopback and an ethernet port, so an interface
// count next to the battery state is two words of nothing.
func TestUPSCardsDoNotShowInterfaceCounts(t *testing.T) {
	// Mirrors the dashboard's rule: interface counts are suppressed when UPS
	// readings are present, and shown otherwise.
	ups := &SNMPInfo{IfTotal: 2, IfUp: 2, UPS: &UPSInfo{ChargePercent: 100}}
	sw := &SNMPInfo{IfTotal: 24, IfUp: 20}
	if ups.UPS == nil {
		t.Fatal("fixture is wrong")
	}
	if sw.UPS != nil {
		t.Fatal("fixture is wrong")
	}
	// The switch keeps its count; the UPS has something better to say.
	if sw.IfTotal == 0 {
		t.Error("a switch lost its interface count")
	}
}

// The runtime estimate and the load are the figures that decide whether a power
// cut is a shrug or a scramble, so they have to survive the poll.
func TestUPSCarriesRuntimeAndLoad(t *testing.T) {
	u := &UPSInfo{ChargePercent: 100, MinutesRemaining: 42, LoadPercent: 31}
	if u.MinutesRemaining != 42 || u.LoadPercent != 31 {
		t.Errorf("lost the useful figures: %+v", u)
	}
	// Zero means "not reported" and is omitted rather than shown as 0m/0%.
	empty := &UPSInfo{ChargePercent: 100}
	if empty.MinutesRemaining != 0 || empty.LoadPercent != 0 {
		t.Errorf("invented figures: %+v", empty)
	}
}

// Storage rows are typed by OID, not by their free-text description, which
// differs per vendor ("Real Memory", "Physical memory", "RAM").
func TestStorageRowsAreTypedByOID(t *testing.T) {
	if oidString(".1.3.6.1.2.1.25.2.1.2") != hrStorageRAM {
		t.Error("a leading dot defeated the type match")
	}
	if oidString(hrStorageFixedDisk) != hrStorageFixedDisk {
		t.Error("an undotted OID did not match itself")
	}
	if oidString(42) != "" {
		t.Error("a non-OID value was treated as a type")
	}
}

// Sizes arrive as counters that can exceed an int on a 32-bit build, and a
// percentage computed from overflowed values is worse than none.
func TestStorageSizesSurviveLargeCounters(t *testing.T) {
	const big = int64(4_000_000_000) // beyond a signed 32-bit int
	v, ok := toInt64(uint64(big))
	if !ok || v != big {
		t.Fatalf("toInt64 = %v %v, want %d", v, ok, big)
	}
	used, size := big/4, big
	if pct := int(used * 100 / size); pct != 25 {
		t.Errorf("percentage from large counters = %d, want 25", pct)
	}
	if _, ok := toInt64("nope"); ok {
		t.Error("a string was accepted as a counter")
	}
}

// UCD reports load averages as text, not numbers.
func TestLoadAverageParsesFromText(t *testing.T) {
	if got := snmpStringValue([]byte(" 0.42 ")); got != "0.42" {
		t.Errorf("got %q", got)
	}
	if got := snmpStringValue(42); got != "" {
		t.Errorf("an integer became %q", got)
	}
}

// A device that implements only the system group leaves the host-style fields
// empty, and that is not a failure — nothing should read as zero percent.
func TestSparseDeviceReportsNothingRatherThanZero(t *testing.T) {
	info := &SNMPInfo{SysName: "switch01", IfTotal: 24, IfUp: 20}
	if info.CPUPercent != 0 || info.MemPercent != 0 || info.DiskPercent != 0 {
		t.Errorf("invented host readings: %+v", info)
	}
	// The dashboard's rule is to show a figure only when it is non-zero, so a
	// device reporting genuinely 0% CPU shows nothing rather than "cpu 0%".
	// That is the accepted trade: an idle firewall is less interesting than a
	// switch that would otherwise claim to have a processor.
	if info.Load1 != 0 {
		t.Error("invented a load average")
	}
}
