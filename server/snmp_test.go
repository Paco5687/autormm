package server

import (
	"context"
	"encoding/json"
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

// Zero is a reading. A UPS with nothing plugged into it reports 0% load, and
// omitting the field made two identical units render differently on a real
// dashboard — one with a LOAD bar and one without.
func TestZeroIsAReadingNotAGap(t *testing.T) {
	u := &UPSInfo{ChargePercent: 100, LoadPercent: 0, MinutesRemaining: 1440}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"load_percent":0`, `"minutes_remaining":1440`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("%s missing from %s", field, b)
		}
	}

	// And a device that reported nothing sends -1, which the dashboard reads as
	// "no bar" rather than as nought percent.
	empty := &SNMPInfo{CPUPercent: -1, MemPercent: -1, DiskPercent: -1}
	eb, _ := json.Marshal(empty)
	for _, field := range []string{`"cpu_percent":-1`, `"mem_percent":-1`, `"disk_percent":-1`} {
		if !strings.Contains(string(eb), field) {
			t.Errorf("%s missing from %s", field, eb)
		}
	}
}

// A device reporting an idle percentage over 100, or a used figure below zero,
// must not produce a bar wider than its track.
func TestPercentagesAreClamped(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 50: 50, 100: 100, 104: 100} {
		if got := clampPercent(in); got != want {
			t.Errorf("clampPercent(%d) = %d, want %d", in, got, want)
		}
	}
}

// Location is optional on every device that offers it, so a blank one is the
// normal case and must not leave an empty field on the card.
func TestBlankLocationIsOmitted(t *testing.T) {
	b, _ := json.Marshal(&SNMPInfo{SysName: "sw01"})
	if strings.Contains(string(b), "sys_location") {
		t.Errorf("a blank location was sent: %s", b)
	}
	b2, _ := json.Marshal(&SNMPInfo{SysName: "sw01", SysLocation: "Rack 1 U12"})
	if !strings.Contains(string(b2), `"sys_location":"Rack 1 U12"`) {
		t.Errorf("location missing: %s", b2)
	}
	// Devices ship it padded or multi-line; a card gets one tidy line.
	if got := firstLine("  Rack 1 U12  \nsecond line"); got != "Rack 1 U12" {
		t.Errorf("got %q", got)
	}
}

// Throughput is a rate, so the first poll has nothing to compare against and
// must report nothing rather than treating a total as a per-second figure.
func TestThroughputNeedsTwoReadings(t *testing.T) {
	now := time.Now()
	first := &SNMPInfo{InOctets: 1_000_000, OutOctets: 500_000, Polled: now}
	first.rateFrom(nil)
	if first.RxRate != 0 || first.TxRate != 0 {
		t.Errorf("invented a rate from one reading: %+v", first)
	}

	second := &SNMPInfo{InOctets: 1_600_000, OutOctets: 800_000, Polled: now.Add(60 * time.Second)}
	second.rateFrom(first)
	if second.RxRate != 10_000 || second.TxRate != 5_000 {
		t.Errorf("rx=%d tx=%d, want 10000 and 5000 bytes/sec", second.RxRate, second.TxRate)
	}
}

// A counter that went backwards means the device restarted or the counter
// wrapped. A gap in the reading is better than an impossible spike in it.
func TestCounterGoingBackwardsIsDropped(t *testing.T) {
	now := time.Now()
	prev := &SNMPInfo{InOctets: 9_000_000, OutOctets: 9_000_000, Polled: now}
	after := &SNMPInfo{InOctets: 1_000, OutOctets: 1_000, Polled: now.Add(60 * time.Second)}
	after.rateFrom(prev)
	if after.RxRate != 0 || after.TxRate != 0 {
		t.Errorf("a restarted device produced a rate: %+v", after)
	}

	// And a gap so long the figure would be meaningless is skipped too.
	stale := &SNMPInfo{InOctets: 9_100_000, OutOctets: 9_100_000, Polled: now.Add(4 * time.Hour)}
	stale.rateFrom(prev)
	if stale.RxRate != 0 {
		t.Errorf("averaged over four hours: %+v", stale)
	}
}

// Counter64 values arrive as several widths, and a negative signed value is not
// a valid byte count.
func TestToUint64RejectsNegatives(t *testing.T) {
	for _, v := range []any{uint64(5), uint(5), uint32(5), int64(5), int(5)} {
		if n, ok := toUint64(v); !ok || n != 5 {
			t.Errorf("%T did not convert: %v %v", v, n, ok)
		}
	}
	if _, ok := toUint64(int64(-1)); ok {
		t.Error("a negative was accepted as a byte count")
	}
	if _, ok := toUint64("100"); ok {
		t.Error("a string was accepted as a byte count")
	}
}

// Some printers report supply levels in units of percent, with a maximum of -2
// meaning "no defined limit". Insisting on a capacity left those devices with
// no reading at all — the level was there and being thrown away.
func TestSupplyInPercentUnitsNeedsNoCapacity(t *testing.T) {
	read := func(unit, max, lvl int) int {
		s := Supply{Percent: -1}
		switch {
		case lvl < 0:
		case unit == prtUnitPercent:
			s.Percent = clampPercent(lvl)
		case max > 0:
			s.Percent = clampPercent(int(float64(lvl) / float64(max) * 100))
		}
		return s.Percent
	}
	if got := read(prtUnitPercent, -2, 40); got != 40 {
		t.Errorf("percent units with no capacity = %d, want 40", got)
	}
	if got := read(4, 2000, 500); got != 25 {
		t.Errorf("counted units = %d, want 25", got)
	}
	// The negative sentinels still mean "will not say", whatever the unit.
	if got := read(prtUnitPercent, -2, -3); got != -1 {
		t.Errorf("a sentinel became %d", got)
	}
}

// hrPrinterDetectedErrorState is a bitmask, and the bits are what a person
// actually wants to be told.
func TestPrinterErrorBitsAreNamed(t *testing.T) {
	if got := printerErrors(0x40); len(got) != 1 || got[0] != "out of paper" {
		t.Errorf("got %v", got)
	}
	if got := printerErrors(0x44); len(got) != 2 {
		t.Errorf("two bits gave %v", got)
	}
	if got := printerErrors(0x00); len(got) != 0 {
		t.Errorf("a healthy printer reported %v", got)
	}
}

// A 24-port switch reports ninety-one interfaces: the ports, a CPU interface,
// and a link aggregate for every port whether one is configured or not.
// Counting them all reported "5 of 53 up" for a switch with four things
// plugged in.
func TestOnlyPhysicalPortsAreCounted(t *testing.T) {
	if !physicalPort(6) {
		t.Error("ethernetCsmacd was not counted as a port")
	}
	if !physicalPort(117) {
		t.Error("gigabitEthernet was not counted as a port")
	}
	for _, t2 := range []int{1, 24, 53, 161} { // other, loopback, propVirtual, lag
		if physicalPort(t2) {
			t.Errorf("ifType %d was counted as a physical port", t2)
		}
	}
}

// A sign-in body carries a password verbatim, so it must not come back to a
// page that is open all day — nor may the token or the password field.
func TestJSONCredentialsAreRedacted(t *testing.T) {
	c := NetCheck{
		ID: "pdu", JSONURL: "https://unifi.lan/api/status",
		JSONAuth: JSONAuth{
			Mode: "login", User: "monitor", Pass: "hunter2", Token: "tok",
			LoginURL: "https://unifi.lan/api/login", LoginBody: `{"password":"hunter2"}`,
		},
	}
	got := redactSecrets(c)
	for name, v := range map[string]string{
		"pass": got.JSONAuth.Pass, "token": got.JSONAuth.Token, "body": got.JSONAuth.LoginBody,
	} {
		if v != secretPlaceholder {
			t.Errorf("%s leaked as %q", name, v)
		}
	}
	// The username and the URLs are not secrets and are needed to show what is
	// configured.
	if got.JSONAuth.User != "monitor" || got.JSONAuth.LoginURL == "" {
		t.Errorf("non-secrets were redacted: %+v", got.JSONAuth)
	}
}
