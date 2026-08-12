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
	SysName  string `json:"sys_name,omitempty"`
	SysDescr string `json:"sys_descr,omitempty"`
	// SysLocation is whatever was typed into the device's own Location field.
	// Omitted when blank, which is the usual case: it is optional on every
	// device that offers it, and an empty row on a card is worse than none.
	SysLocation string   `json:"sys_location,omitempty"`
	UptimeSecs  int64    `json:"uptime_secs,omitempty"`
	IfTotal     int      `json:"if_total,omitempty"`
	IfUp        int      `json:"if_up,omitempty"`
	Supplies    []Supply `json:"supplies,omitempty"` // printer toner / ink
	UPS         *UPSInfo `json:"ups,omitempty"`
	// Host-style readings, where the device implements Host Resources or UCD.
	//
	// -1 means the device did not report it, and is sent rather than omitted:
	// zero is a real reading — an idle firewall, a UPS with nothing plugged into
	// it — and a field left out is indistinguishable from one that came back 0.
	CPUPercent  int     `json:"cpu_percent"`
	MemPercent  int     `json:"mem_percent"`
	DiskPercent int     `json:"disk_percent"`
	DiskName    string  `json:"disk_name,omitempty"` // the fullest filesystem
	Load1       float64 `json:"load1,omitempty"`
	// PFStates is pfSense's state table occupancy, and PFStateLimit its ceiling.
	PFStates     int `json:"pf_states,omitempty"`
	PFStateLimit int `json:"pf_state_limit,omitempty"`
	// PoE draw against capacity, in watts, for a switch that reports it.
	PoEWatts    int `json:"poe_watts,omitempty"`
	PoECapacity int `json:"poe_capacity,omitempty"`
	// Stations is how many wireless clients an access point is carrying.
	Stations int `json:"stations,omitempty"`
	// PageCount is a printer's lifetime page total, and PrinterErrors whatever
	// it says is currently wrong with it.
	PageCount     int      `json:"page_count,omitempty"`
	PrinterErrors []string `json:"printer_errors,omitempty"`
	// Traffic in bytes per second, worked out from the interface counters
	// between two polls. Absent on the first, since a rate needs two readings.
	RxRate int64 `json:"rx_rate,omitempty"`
	TxRate int64 `json:"tx_rate,omitempty"`
	// Raw totals, kept so the next poll can work out the rate. Not sent: the
	// dashboard wants the rate, and these change every few seconds.
	InOctets  uint64 `json:"-"`
	OutOctets uint64 `json:"-"`

	Polled time.Time `json:"polled"`
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
	MinutesRemaining int `json:"minutes_remaining"`
	// LoadPercent is output load against capacity: what tells you whether the
	// runtime estimate will hold, and whether the thing is overloaded.
	LoadPercent int `json:"load_percent"`
}

// Standard OIDs. Named rather than inlined because a mistyped OID returns
// "no such object" rather than anything that looks wrong.
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysUptime   = "1.3.6.1.2.1.1.3.0" // TimeTicks, hundredths of a second
	oidSysName     = "1.3.6.1.2.1.1.5.0"
	oidSysLocation = "1.3.6.1.2.1.1.6.0"

	oidIfOperStatus = "1.3.6.1.2.1.2.2.1.8" // 1 = up, 2 = down
	oidIfType       = "1.3.6.1.2.1.2.2.1.3" // 6 = a real ethernet port

	oidSupplyDesc  = "1.3.6.1.2.1.43.11.1.1.6"
	oidSupplyUnit  = "1.3.6.1.2.1.43.11.1.1.7" // 19 = the level is already a percentage
	oidSupplyMax   = "1.3.6.1.2.1.43.11.1.1.8"
	oidSupplyLevel = "1.3.6.1.2.1.43.11.1.1.9"

	// Pages printed over the device's life, and what it says is wrong with it.
	oidPrtLifeCount  = "1.3.6.1.2.1.43.10.2.1.4"
	oidPrtStatus     = "1.3.6.1.2.1.25.3.5.1.1" // 3 idle, 4 printing, 5 warmup
	oidPrtErrorState = "1.3.6.1.2.1.25.3.5.1.2" // bitmask: jam, no paper, door…

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

	// UCD. Load average comes back as a string rather than a number, and the
	// rest of this group is how a BSD reports what Host Resources does not:
	// FreeBSD's bsnmpd, which is what pfSense runs, implements the storage
	// table but not hrProcessorLoad, so CPU has to come from here.
	oidUCDLoad1     = "1.3.6.1.4.1.2021.10.1.3.1"
	oidUCDCpuIdle   = "1.3.6.1.4.1.2021.11.11.0"
	oidUCDMemTotal  = "1.3.6.1.4.1.2021.4.5.0"
	oidUCDMemAvail  = "1.3.6.1.4.1.2021.4.6.0"
	oidUCDMemBuffer = "1.3.6.1.4.1.2021.4.14.0"
	oidUCDMemCached = "1.3.6.1.4.1.2021.4.15.0"
	oidUCDDiskPath  = "1.3.6.1.4.1.2021.9.1.2"
	oidUCDDiskPct   = "1.3.6.1.4.1.2021.9.1.9"

	// POWER-ETHERNET-MIB (RFC 3621): what a PoE switch is delivering against
	// what it can. A 250W switch quietly approaching its budget is the failure
	// that takes cameras and phones down one at a time.
	oidPethPower    = "1.3.6.1.2.1.105.1.3.1.1.2" // nominal capacity, watts
	oidPethConsumed = "1.3.6.1.2.1.105.1.3.1.1.4" // actual draw, watts

	// Interface counters, for throughput. The 64-bit forms, because the 32-bit
	// ones wrap every few minutes on a gigabit link and a wrapped counter reads
	// as a negative rate or an enormous one.
	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"

	// Ubiquiti's own MIB. Client count is the one number that says whether an
	// access point is doing anything, and no standard MIB carries it.
	oidUnifiVapStations = "1.3.6.1.4.1.41112.1.6.1.2.1.8"

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

	info := &SNMPInfo{
		Polled: time.Now(), Version: versionName(version),
		CPUPercent: -1, MemPercent: -1, DiskPercent: -1,
	}

	// The system group first: if this fails the device is not answering SNMP at
	// all, and the rest would just be three more timeouts.
	res, err := g.Get([]string{oidSysDescr, oidSysUptime, oidSysName, oidSysLocation})
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
		case oidSysLocation:
			info.SysLocation = firstLine(snmpString(v))
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
	pollPrinter(g, info)
	pollPoE(g, info)
	pollStations(g, info)
	pollOctets(g, info)
	return info
}

// pollInterfaces counts how many physical ports are up.
//
// Physical ones only. A 24-port switch reports ninety-one interfaces — the
// ports, a CPU interface, and a link aggregate for every port whether or not
// one is configured — and counting them all gives "5 of 53 up" for a switch
// with four things plugged into it. A firewall does the same with loopback and
// pflog. ifType 6 is ethernetCsmacd, which is what a port is.
func pollInterfaces(g *gosnmp.GoSNMP) (total, up int) {
	rows, err := walk(g, oidIfOperStatus)
	if err != nil {
		return 0, 0
	}
	types := indexBySuffix(mustWalk(g, oidIfType))
	for _, v := range rows {
		n, ok := toInt(v.Value)
		if !ok {
			continue
		}
		// A device that does not report types at all is counted whole rather
		// than not at all: fewer devices report ifType than ifOperStatus.
		if t, known := toInt(types[suffix(v.Name)]); known && !physicalPort(t) {
			continue
		}
		total++
		if n == 1 { // up
			up++
		}
	}
	return total, up
}

// physicalPort reports whether an ifType is something with a socket on it.
func physicalPort(t int) bool {
	switch t {
	case 6, 117: // ethernetCsmacd, and the deprecated gigabitEthernet
		return true
	}
	return false
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
	units, _ := walk(g, oidSupplyUnit)

	maxBy := indexBySuffix(maxes)
	lvlBy := indexBySuffix(levels)
	unitBy := indexBySuffix(units)

	var out []Supply
	for _, d := range descs {
		idx := suffix(d.Name)
		s := Supply{Name: snmpString(d), Percent: -1}
		if s.Name == "" {
			s.Name = "supply " + idx
		}
		max, okMax := toInt(maxBy[idx])
		lvl, okLvl := toInt(lvlBy[idx])
		unit, _ := toInt(unitBy[idx])
		switch {
		case !okLvl || lvl < 0:
			// A negative level is one of the MIB's sentinels, not a quantity.
		case unit == prtUnitPercent:
			// The level is already a percentage. Insisting on a capacity here
			// is what left an HP reporting in percent with no reading at all:
			// those models publish a maximum of -2, meaning "no defined limit",
			// which is not a number to divide by.
			s.Percent = clampPercent(lvl)
		case okMax && max > 0:
			s.Percent = clampPercent(int(float64(lvl) / float64(max) * 100))
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// prtUnitPercent is prtMarkerSuppliesSupplyUnit's "percent" value, where the
// level column carries a percentage rather than a count of something.
const prtUnitPercent = 19

// pollPrinter reads what a printer says about itself beyond its consumables:
// how much it has printed, and what it thinks is wrong.
func pollPrinter(g *gosnmp.GoSNMP, info *SNMPInfo) {
	for _, r := range mustWalk(g, oidPrtLifeCount) {
		if n, ok := toInt(r.Value); ok && n > info.PageCount {
			info.PageCount = n
		}
	}
	for _, r := range mustWalk(g, oidPrtErrorState) {
		// An OctetString bitmask; the first byte carries everything worth
		// reporting on a device that is otherwise reachable and quiet.
		b, ok := r.Value.([]byte)
		if !ok || len(b) == 0 {
			continue
		}
		info.PrinterErrors = append(info.PrinterErrors, printerErrors(b[0])...)
	}
}

// printerErrors names the bits set in hrPrinterDetectedErrorState.
func printerErrors(b byte) []string {
	names := []struct {
		bit  byte
		name string
	}{
		{0x80, "low paper"}, {0x40, "out of paper"},
		{0x20, "low toner"}, {0x10, "out of toner"},
		{0x08, "door open"}, {0x04, "jammed"},
		{0x02, "offline"}, {0x01, "service requested"},
	}
	var out []string
	for _, n := range names {
		if b&n.bit != 0 {
			out = append(out, n.name)
		}
	}
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
	u := &UPSInfo{ChargePercent: -1, LoadPercent: -1, MinutesRemaining: -1}
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

// pollUCD reads load, and fills in whatever Host Resources did not.
//
// The fallbacks are what make a pfSense box report like a host: bsnmpd does not
// implement hrProcessorLoad, so without this a firewall shows a state table and
// a load average and nothing else. Each only applies where the reading is still
// unset, so a device implementing both is read from the standard MIB.
func pollUCD(g *gosnmp.GoSNMP, info *SNMPInfo) {
	res, err := g.Get([]string{
		oidUCDLoad1, oidUCDCpuIdle, oidUCDMemTotal, oidUCDMemAvail,
		oidUCDMemBuffer, oidUCDMemCached,
	})
	if err != nil {
		return
	}
	var memTotal, memAvail, memBuffer, memCached int64
	for _, v := range res.Variables {
		switch strings.TrimPrefix(v.Name, ".") {
		case oidUCDLoad1:
			if f, err := strconv.ParseFloat(snmpStringValue(v.Value), 64); err == nil {
				info.Load1 = f
			}
		case oidUCDCpuIdle:
			if n, ok := toInt(v.Value); ok && info.CPUPercent < 0 {
				info.CPUPercent = clampPercent(100 - n)
			}
		case oidUCDMemTotal:
			memTotal, _ = toInt64(v.Value)
		case oidUCDMemAvail:
			memAvail, _ = toInt64(v.Value)
		case oidUCDMemBuffer:
			memBuffer, _ = toInt64(v.Value)
		case oidUCDMemCached:
			memCached, _ = toInt64(v.Value)
		}
	}
	if info.MemPercent < 0 && memTotal > 0 {
		// Buffers and cache count as available, which is the conventional
		// reading: memory holding a disk cache is not memory in use. A device
		// that does not report them returns zero and the sum is unaffected.
		free := memAvail + memBuffer + memCached
		if free > memTotal {
			free = memTotal
		}
		info.MemPercent = clampPercent(int((memTotal - free) * 100 / memTotal))
	}
	if info.DiskPercent < 0 {
		pollUCDDisks(g, info)
	}
}

// pollUCDDisks reads the UCD disk table, for devices whose storage does not
// appear in Host Resources.
func pollUCDDisks(g *gosnmp.GoSNMP, info *SNMPInfo) {
	pcts, err := walk(g, oidUCDDiskPct)
	if err != nil || len(pcts) == 0 {
		return
	}
	paths := indexBySuffix(mustWalk(g, oidUCDDiskPath))
	for _, r := range pcts {
		n, ok := toInt(r.Value)
		if !ok || n < 0 {
			continue
		}
		if n > info.DiskPercent {
			info.DiskPercent, info.DiskName = clampPercent(n), snmpStringValue(paths[suffix(r.Name)])
		}
	}
}

// clampPercent keeps a reading inside the range a percentage can occupy: a
// device reporting 104% idle or a negative used figure should not become a bar
// wider than its track.
func clampPercent(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
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

// pollPoE reads what a switch is delivering against its budget.
//
// A table indexed by power-sourcing unit; a switch has one, a chassis may have
// several, and the totals are what matter either way.
func pollPoE(g *gosnmp.GoSNMP, info *SNMPInfo) {
	consumed, err := walk(g, oidPethConsumed)
	if err != nil || len(consumed) == 0 {
		return
	}
	for _, r := range consumed {
		if n, ok := toInt(r.Value); ok && n >= 0 {
			info.PoEWatts += n
		}
	}
	for _, r := range mustWalk(g, oidPethPower) {
		if n, ok := toInt(r.Value); ok && n > 0 {
			info.PoECapacity += n
		}
	}
}

// pollStations counts the wireless clients an access point is carrying, summed
// across its virtual APs. Vendor-specific to Ubiquiti; absent everywhere else.
func pollStations(g *gosnmp.GoSNMP, info *SNMPInfo) {
	rows, err := walk(g, oidUnifiVapStations)
	if err != nil {
		return
	}
	for _, r := range rows {
		if n, ok := toInt(r.Value); ok && n > 0 {
			info.Stations += n
		}
	}
}

// pollOctets totals the interface byte counters, which the caller turns into a
// rate by comparing against the previous poll.
//
// Only the 64-bit counters: the 32-bit ones wrap every few minutes on a gigabit
// link, and a wrapped counter reads either as a negative rate or an enormous
// one. A device too old to have them simply reports no throughput.
func pollOctets(g *gosnmp.GoSNMP, info *SNMPInfo) {
	for _, r := range mustWalk(g, oidIfHCInOctets) {
		if n, ok := toUint64(r.Value); ok {
			info.InOctets += n
		}
	}
	for _, r := range mustWalk(g, oidIfHCOutOctets) {
		if n, ok := toUint64(r.Value); ok {
			info.OutOctets += n
		}
	}
}

// rateFrom fills in throughput by comparing this poll's counters with the last.
//
// A counter that went backwards means the device restarted or the counters
// wrapped; the reading is dropped rather than turned into a spike, because a
// graph with one impossible value in it is worse than a graph with a gap.
func (info *SNMPInfo) rateFrom(prev *SNMPInfo) {
	if prev == nil || prev.InOctets == 0 || prev.Polled.IsZero() {
		return
	}
	secs := info.Polled.Sub(prev.Polled).Seconds()
	if secs <= 0 || secs > 3600 {
		return
	}
	if info.InOctets >= prev.InOctets {
		info.RxRate = int64(float64(info.InOctets-prev.InOctets) / secs)
	}
	if info.OutOctets >= prev.OutOctets {
		info.TxRate = int64(float64(info.OutOctets-prev.OutOctets) / secs)
	}
}

// toUint64 handles the counter64 values the high-capacity octet counters use.
func toUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint:
		return uint64(n), true
	case uint32:
		return uint64(n), true
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
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
