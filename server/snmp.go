package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMP polling for agentless devices.
//
// A TCP check can only say "something answered on port 443", which for a
// switch, a printer or a UPS is nearly no information: the switch is reachable
// while half its ports are down, the printer is reachable with no toner, and
// the UPS is reachable precisely while it runs the rack off its battery. SNMP
// is how that gear has always reported what it is actually doing.
//
// v1 and v2c, read-only. v3 brings user/auth/privacy configuration that homelab
// gear mostly does not have turned on, and writing to a device over a community
// string is not something this hub should be able to do at all.
//
// Both versions matter in practice: plenty of gear — APC network management
// cards among them — offers only v1, and a v2c request to a v1-only agent is
// not answered at all. It looks exactly like a wrong community string.

// SNMPInfo is what one poll learned. Every field is optional: a switch answers
// the interface questions and not the printer ones, and that is not a failure.
type SNMPInfo struct {
	SysName    string   `json:"sys_name,omitempty"`
	SysDescr   string   `json:"sys_descr,omitempty"`
	UptimeSecs int64    `json:"uptime_secs,omitempty"`
	IfTotal    int      `json:"if_total,omitempty"`
	IfUp       int      `json:"if_up,omitempty"`
	Supplies   []Supply `json:"supplies,omitempty"` // printer toner / ink
	UPS        *UPSInfo `json:"ups,omitempty"`
	// Host-style readings, where the device implements Host Resources or UCD.
	// -1 means the device did not report it, which is not the same as zero.
	CPUPercent  int     `json:"cpu_percent,omitempty"`
	MemPercent  int     `json:"mem_percent,omitempty"`
	DiskPercent int     `json:"disk_percent,omitempty"`
	DiskName    string  `json:"disk_name,omitempty"` // the fullest filesystem
	Load1       float64 `json:"load1,omitempty"`
	// PFStates is pfSense's state table occupancy, and PFStateLimit its ceiling.
	PFStates     int       `json:"pf_states,omitempty"`
	PFStateLimit int       `json:"pf_state_limit,omitempty"`
	Polled       time.Time `json:"polled"`
	// Version is which protocol version actually answered, which is worth
	// showing when the setting was left on automatic.
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Supply is one printer consumable.
type Supply struct {
	Name    string `json:"name"`
	Percent int    `json:"percent"` // -1 when the device declines to say
}

// UPSInfo is the part of the UPS MIB worth waking someone for.
type UPSInfo struct {
	ChargePercent    int  `json:"charge_percent"`
	OnBattery        bool `json:"on_battery"`
	SecondsOnBattery int  `json:"seconds_on_battery,omitempty"`
	BatteryLow       bool `json:"battery_low"`
	// MinutesRemaining is the UPS's own estimate of runtime left, which is the
	// figure that answers "do I have time to shut things down properly".
	MinutesRemaining int `json:"minutes_remaining,omitempty"`
	// LoadPercent is output load against capacity: what tells you whether the
	// runtime estimate will hold, and whether the thing is overloaded.
	LoadPercent int `json:"load_percent,omitempty"`
}

// Standard OIDs. Named rather than inlined because a mistyped OID returns
// "no such object" rather than anything that looks wrong.
const (
	oidSysDescr  = "1.3.6.1.2.1.1.1.0"
	oidSysUptime = "1.3.6.1.2.1.1.3.0" // TimeTicks, hundredths of a second
	oidSysName   = "1.3.6.1.2.1.1.5.0"

	oidIfOperStatus = "1.3.6.1.2.1.2.2.1.8" // 1 = up, 2 = down

	oidSupplyDesc  = "1.3.6.1.2.1.43.11.1.1.6"
	oidSupplyMax   = "1.3.6.1.2.1.43.11.1.1.8"
	oidSupplyLevel = "1.3.6.1.2.1.43.11.1.1.9"

	// Host Resources: the standard way a device reports what a host agent would.
	// Works well beyond pfSense — plenty of switches, NAS units and printers
	// implement it too.
	oidHrProcessorLoad = "1.3.6.1.2.1.25.3.3.1.2"
	oidHrStorageType   = "1.3.6.1.2.1.25.2.3.1.2"
	oidHrStorageDescr  = "1.3.6.1.2.1.25.2.3.1.3"
	oidHrStorageSize   = "1.3.6.1.2.1.25.2.3.1.5"
	oidHrStorageUsed   = "1.3.6.1.2.1.25.2.3.1.6"

	// Storage rows are typed by OID rather than by their description, which is
	// free text and differs per vendor ("Real Memory", "Physical memory", …).
	hrStorageRAM       = "1.3.6.1.2.1.25.2.1.2"
	hrStorageFixedDisk = "1.3.6.1.2.1.25.2.1.4"

	// UCD: load average, reported as strings rather than numbers.
	oidUCDLoad1 = "1.3.6.1.4.1.2021.10.1.3.1"

	// pfSense's PF state table occupancy. Vendor-specific, and the figure a
	// firewall runs out of before it runs out of anything else.
	oidPFStateCount = "1.3.6.1.4.1.12325.1.200.1.3.1.0"
	oidPFStateLimit = "1.3.6.1.4.1.12325.1.200.1.3.2.0"

	oidUPSBatteryStatus = "1.3.6.1.2.1.33.1.2.1.0" // 2 = normal, 3 = low, 4 = depleted
	oidUPSSecondsOnBatt = "1.3.6.1.2.1.33.1.2.2.0"
	oidUPSMinutesRemain = "1.3.6.1.2.1.33.1.2.3.0"
	oidUPSChargeRemain  = "1.3.6.1.2.1.33.1.2.4.0"
	oidUPSOutputLoad    = "1.3.6.1.2.1.33.1.4.4.1.5" // per output line
)

// maxWalkRows bounds a table walk. A switch has tens of ports and a printer a
// handful of cartridges; anything answering with thousands of rows is either
// not what we think it is or not worth the round trips.
const maxWalkRows = 256

// snmpPoll reads what a device will tell us.
//
// version is "1", "2c", or empty for automatic — which tries v2c and falls back
// to v1, because the two are indistinguishable from the outside: a v1-only
// agent simply does not reply to a v2c request, which presents identically to a
// wrong community string.
func snmpPoll(ctx context.Context, host string, port int, creds SNMPCreds) *SNMPInfo {
	switch creds.Version {
	case "1":
		return snmpPollVersion(ctx, host, port, creds, gosnmp.Version1)
	case "2c":
		return snmpPollVersion(ctx, host, port, creds, gosnmp.Version2c)
	case "3":
		return snmpPollVersion(ctx, host, port, creds, gosnmp.Version3)
	}
	info := snmpPollVersion(ctx, host, port, creds, gosnmp.Version2c)
	if info.Error == "" {
		return info
	}
	// Only the fallback's result is kept if it works; otherwise report the
	// first attempt, since "no response" is the more useful message.
	if v1 := snmpPollVersion(ctx, host, port, creds, gosnmp.Version1); v1.Error == "" {
		return v1
	}
	return info
}

// SNMPCreds is everything a poll needs to authenticate.
type SNMPCreds struct {
	Community string
	Version   string
	User      string
	AuthProto string
	AuthPass  string
	PrivProto string
	PrivPass  string
}

func snmpPollVersion(ctx context.Context, host string, port int, creds SNMPCreds, version gosnmp.SnmpVersion) *SNMPInfo {
	if port <= 0 {
		port = 161
	}
	g := &gosnmp.GoSNMP{
		Target:    host,
		Port:      uint16(port),
		Community: creds.Community,
		Version:   version,
		Timeout:   2 * time.Second,
		Retries:   1,
		MaxOids:   gosnmp.MaxOids,
		Context:   ctx,
	}
	if version == gosnmp.Version3 {
		applyV3(g, creds)
	}
	if err := g.Connect(); err != nil {
		return &SNMPInfo{Error: err.Error(), Polled: time.Now()}
	}
	defer g.Conn.Close()

	info := &SNMPInfo{Polled: time.Now(), Version: versionName(version)}

	// The system group first: if this fails the device is not answering SNMP at
	// all, and the rest would just be three more timeouts.
	res, err := g.Get([]string{oidSysDescr, oidSysUptime, oidSysName})
	if err != nil {
		info.Error = snmpError(err)
		return info
	}
	for _, v := range res.Variables {
		switch strings.TrimPrefix(v.Name, ".") {
		case oidSysDescr:
			info.SysDescr = firstLine(snmpString(v))
		case oidSysName:
			info.SysName = snmpString(v)
		case oidSysUptime:
			if t, ok := v.Value.(uint32); ok {
				info.UptimeSecs = int64(t) / 100 // TimeTicks are hundredths
			}
		}
	}

	info.IfTotal, info.IfUp = pollInterfaces(g)
	info.Supplies = pollSupplies(g)
	info.UPS = pollUPS(g)
	pollHostResources(g, info)
	pollUCD(g, info)
	pollPF(g, info)
	return info
}

// pollInterfaces counts how many interfaces are up. The count is the useful
// figure — "20 of 24 up" says more about a switch than any single port does.
func pollInterfaces(g *gosnmp.GoSNMP) (total, up int) {
	rows, err := walk(g, oidIfOperStatus)
	if err != nil {
		return 0, 0
	}
	for _, v := range rows {
		n, ok := toInt(v.Value)
		if !ok {
			continue
		}
		total++
		if n == 1 { // up
			up++
		}
	}
	return total, up
}

// pollSupplies reads printer consumables.
//
// The level column uses negative sentinels rather than a percentage: -1 means
// the printer will not say, -2 means the supply has no meaningful limit, and -3
// means "some remaining" with no figure. Treating those as numbers is how a
// dashboard ends up reporting a cartridge at minus one percent.
func pollSupplies(g *gosnmp.GoSNMP) []Supply {
	descs, err := walk(g, oidSupplyDesc)
	if err != nil || len(descs) == 0 {
		return nil
	}
	maxes, _ := walk(g, oidSupplyMax)
	levels, _ := walk(g, oidSupplyLevel)

	maxBy := indexBySuffix(maxes)
	lvlBy := indexBySuffix(levels)

	var out []Supply
	for _, d := range descs {
		idx := suffix(d.Name)
		s := Supply{Name: snmpString(d), Percent: -1}
		if s.Name == "" {
			s.Name = "supply " + idx
		}
		max, okMax := toInt(maxBy[idx])
		lvl, okLvl := toInt(lvlBy[idx])
		if okMax && okLvl && max > 0 && lvl >= 0 {
			s.Percent = int(float64(lvl) / float64(max) * 100)
			if s.Percent > 100 {
				s.Percent = 100
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pollUPS reads the UPS battery group. Most devices are not a UPS, so every
// error here means "not a UPS" rather than a fault worth reporting.
func pollUPS(g *gosnmp.GoSNMP) *UPSInfo {
	res, err := g.Get([]string{oidUPSBatteryStatus, oidUPSSecondsOnBatt,
		oidUPSMinutesRemain, oidUPSChargeRemain})
	if err != nil {
		return nil
	}
	u := &UPSInfo{ChargePercent: -1}
	answered := false
	for _, v := range res.Variables {
		n, ok := toInt(v.Value)
		if !ok {
			continue
		}
		answered = true
		switch strings.TrimPrefix(v.Name, ".") {
		case oidUPSBatteryStatus:
			u.BatteryLow = n == 3 || n == 4 // batteryLow | batteryDepleted
		case oidUPSSecondsOnBatt:
			u.SecondsOnBattery = n
			u.OnBattery = n > 0
		case oidUPSMinutesRemain:
			u.MinutesRemaining = n
		case oidUPSChargeRemain:
			u.ChargePercent = n
		}
	}
	if !answered {
		return nil
	}
	// Load is a table indexed by output line. The busiest line is the one that
	// matters, and single-phase gear — which is all of it, here — has one.
	if rows, err := walk(g, oidUPSOutputLoad); err == nil {
		for _, r := range rows {
			if n, ok := toInt(r.Value); ok && n > u.LoadPercent {
				u.LoadPercent = n
			}
		}
	}
	return u
}

// pollHostResources reads CPU, memory and disk the way a host agent would.
//
// Every field is optional: a switch implementing only the system group leaves
// all of this empty, which is not a failure.
func pollHostResources(g *gosnmp.GoSNMP, info *SNMPInfo) {
	// Processor load is one row per core; the average is the figure that means
	// what "CPU%" means everywhere else.
	if rows, err := walk(g, oidHrProcessorLoad); err == nil && len(rows) > 0 {
		sum, n := 0, 0
		for _, r := range rows {
			if v, ok := toInt(r.Value); ok {
				sum, n = sum+v, n+1
			}
		}
		if n > 0 {
			info.CPUPercent = sum / n
		}
	}

	types, err := walk(g, oidHrStorageType)
	if err != nil || len(types) == 0 {
		return
	}
	descrs := indexBySuffix(mustWalk(g, oidHrStorageDescr))
	sizes := indexBySuffix(mustWalk(g, oidHrStorageSize))
	useds := indexBySuffix(mustWalk(g, oidHrStorageUsed))

	for _, t := range types {
		idx := suffix(t.Name)
		size, okSize := toInt64(sizes[idx])
		used, okUsed := toInt64(useds[idx])
		if !okSize || !okUsed || size <= 0 {
			continue
		}
		// Allocation units cancel out of a percentage, so they are not needed
		// and cannot overflow anything on the way.
		pct := int(used * 100 / size)
		switch oidString(t.Value) {
		case hrStorageRAM:
			info.MemPercent = pct
		case hrStorageFixedDisk:
			// The fullest filesystem is the one worth reporting; a device with
			// six of them has one that will fill first.
			if pct > info.DiskPercent {
				info.DiskPercent, info.DiskName = pct, snmpStringValue(descrs[idx])
			}
		}
	}
}

// pollUCD reads the load average, which UCD reports as text.
func pollUCD(g *gosnmp.GoSNMP, info *SNMPInfo) {
	res, err := g.Get([]string{oidUCDLoad1})
	if err != nil || len(res.Variables) == 0 {
		return
	}
	if f, err := strconv.ParseFloat(snmpStringValue(res.Variables[0].Value), 64); err == nil {
		info.Load1 = f
	}
}

// pollPF reads pfSense's state table. Silently absent on anything else.
func pollPF(g *gosnmp.GoSNMP, info *SNMPInfo) {
	res, err := g.Get([]string{oidPFStateCount, oidPFStateLimit})
	if err != nil {
		return
	}
	for _, v := range res.Variables {
		n, ok := toInt(v.Value)
		if !ok {
			continue
		}
		switch strings.TrimPrefix(v.Name, ".") {
		case oidPFStateCount:
			info.PFStates = n
		case oidPFStateLimit:
			info.PFStateLimit = n
		}
	}
}

// mustWalk is walk with the error dropped, for the columns that are only
// useful alongside one that already succeeded.
func mustWalk(g *gosnmp.GoSNMP, root string) []gosnmp.SnmpPDU {
	rows, _ := walk(g, root)
	return rows
}

// toInt64 handles the counter widths that storage sizes arrive as, which can
// exceed what an int holds on a 32-bit build.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	}
	return 0, false
}

// oidString renders an ObjectIdentifier value, which is how a storage row says
// what kind of storage it is.
func oidString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimPrefix(s, ".")
}

// snmpStringValue is snmpString for a bare value rather than a whole PDU.
func snmpStringValue(v any) string {
	switch s := v.(type) {
	case []byte:
		return strings.TrimSpace(string(s))
	case string:
		return strings.TrimSpace(s)
	}
	return ""
}

// walk reads a table, bounded so an unexpected device cannot hold the poll open.
//
// GETBULK is a v2c operation and does not exist in v1, where the equivalent is
// a sequence of GETNEXTs — one round trip per row rather than per batch, which
// is slower but is what a v1 device understands.
func walk(g *gosnmp.GoSNMP, root string) ([]gosnmp.SnmpPDU, error) {
	walker := g.BulkWalk
	if g.Version == gosnmp.Version1 {
		walker = g.Walk
	}
	var out []gosnmp.SnmpPDU
	err := walker(root, func(pdu gosnmp.SnmpPDU) error {
		if len(out) >= maxWalkRows {
			return fmt.Errorf("too many rows")
		}
		out = append(out, pdu)
		return nil
	})
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// indexBySuffix keys a table's rows by their trailing index, so two columns of
// the same table can be lined up.
func indexBySuffix(rows []gosnmp.SnmpPDU) map[string]any {
	m := make(map[string]any, len(rows))
	for _, r := range rows {
		m[suffix(r.Name)] = r.Value
	}
	return m
}

// suffix is the row index of an OID: the part after the column.
func suffix(oid string) string {
	if i := strings.LastIndexByte(oid, '.'); i >= 0 {
		return oid[i+1:]
	}
	return oid
}

// snmpString renders an OctetString, which gosnmp hands back as bytes.
func snmpString(v gosnmp.SnmpPDU) string {
	switch s := v.Value.(type) {
	case []byte:
		return strings.TrimSpace(string(s))
	case string:
		return strings.TrimSpace(s)
	}
	return ""
}

// toInt accepts the several integer widths SNMP values arrive as.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	}
	return 0, false
}

// firstLine trims a sysDescr to something a card can show: they routinely run
// to several lines of kernel build detail.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return strings.TrimSpace(s)
}

// applyV3 configures the user security model.
//
// The security level follows from what was supplied rather than being a fourth
// thing to set: a passphrase for both means authPriv, auth only means
// authNoPriv, neither means noAuthNoPriv. Choosing it separately from the
// passphrases is how people end up with a device configured for authPriv and a
// poller asking in the clear.
func applyV3(g *gosnmp.GoSNMP, c SNMPCreds) {
	g.SecurityModel = gosnmp.UserSecurityModel
	usm := &gosnmp.UsmSecurityParameters{UserName: c.User}

	switch {
	case c.AuthPass != "" && c.PrivPass != "":
		g.MsgFlags = gosnmp.AuthPriv
	case c.AuthPass != "":
		g.MsgFlags = gosnmp.AuthNoPriv
	default:
		g.MsgFlags = gosnmp.NoAuthNoPriv
	}
	if c.AuthPass != "" {
		usm.AuthenticationProtocol = authProto(c.AuthProto)
		usm.AuthenticationPassphrase = c.AuthPass
	}
	if c.PrivPass != "" {
		usm.PrivacyProtocol = privProto(c.PrivProto)
		usm.PrivacyPassphrase = c.PrivPass
	}
	g.SecurityParameters = usm
}

// authProto and privProto default to the stronger of the common choices, since
// a blank setting more often means "not thought about" than "use the weakest".
func authProto(name string) gosnmp.SnmpV3AuthProtocol {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "MD5":
		return gosnmp.MD5
	case "SHA256":
		return gosnmp.SHA256
	case "SHA512":
		return gosnmp.SHA512
	}
	return gosnmp.SHA
}

func privProto(name string) gosnmp.SnmpV3PrivProtocol {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DES":
		return gosnmp.DES
	case "AES192":
		return gosnmp.AES192
	case "AES256":
		return gosnmp.AES256
	}
	return gosnmp.AES
}

func versionName(v gosnmp.SnmpVersion) string {
	switch v {
	case gosnmp.Version1:
		return "v1"
	case gosnmp.Version3:
		return "v3"
	}
	return "v2c"
}

// snmpError shortens the library's errors to something a card can show.
func snmpError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return "no SNMP response — check the community string, that SNMP is enabled, and that the hub's address is allowed to query it"
	}
	return msg
}

// snmpConfigured reports whether this check has enough to attempt a poll.
//
// v1 and v2c need a community; v3 needs a username and nothing else, since
// noAuthNoPriv is a legitimate (if unwise) configuration.
func snmpConfigured(c NetCheck) bool {
	if c.SNMPVersion == "3" {
		return c.SNMPUser != ""
	}
	return c.SNMP != ""
}

// snmpCreds gathers everything a poll needs from a check.
func snmpCreds(c NetCheck) SNMPCreds {
	return SNMPCreds{
		Community: c.SNMP,
		Version:   c.SNMPVersion,
		User:      c.SNMPUser,
		AuthProto: c.SNMPAuthProto,
		AuthPass:  c.SNMPAuthPass,
		PrivProto: c.SNMPPrivProto,
		PrivPass:  c.SNMPPrivPass,
	}
}
