package server

import (
	"context"
	"fmt"
	"sort"
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
// v2c only, and read-only GETs. v3 brings user/auth/privacy configuration that
// homelab gear mostly does not have turned on, and writing to a device over a
// community string is not something this hub should be able to do at all.

// SNMPInfo is what one poll learned. Every field is optional: a switch answers
// the interface questions and not the printer ones, and that is not a failure.
type SNMPInfo struct {
	SysName    string    `json:"sys_name,omitempty"`
	SysDescr   string    `json:"sys_descr,omitempty"`
	UptimeSecs int64     `json:"uptime_secs,omitempty"`
	IfTotal    int       `json:"if_total,omitempty"`
	IfUp       int       `json:"if_up,omitempty"`
	Supplies   []Supply  `json:"supplies,omitempty"` // printer toner / ink
	UPS        *UPSInfo  `json:"ups,omitempty"`
	Polled     time.Time `json:"polled"`
	Error      string    `json:"error,omitempty"`
}

// Supply is one printer consumable.
type Supply struct {
	Name    string `json:"name"`
	Percent int    `json:"percent"` // -1 when the device declines to say
}

// UPSInfo is the part of the UPS MIB worth waking someone for.
type UPSInfo struct {
	ChargePercent   int  `json:"charge_percent"`
	OnBattery       bool `json:"on_battery"`
	SecondsOnBattery int `json:"seconds_on_battery,omitempty"`
	BatteryLow      bool `json:"battery_low"`
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

	oidUPSBatteryStatus  = "1.3.6.1.2.1.33.1.2.1.0" // 2 = normal, 3 = low, 4 = depleted
	oidUPSSecondsOnBatt  = "1.3.6.1.2.1.33.1.2.2.0"
	oidUPSChargeRemain   = "1.3.6.1.2.1.33.1.2.4.0"
)

// maxWalkRows bounds a table walk. A switch has tens of ports and a printer a
// handful of cartridges; anything answering with thousands of rows is either
// not what we think it is or not worth the round trips.
const maxWalkRows = 256

// snmpPoll reads what a device will tell us. A device that answers the system
// group but nothing else still returns a useful result.
func snmpPoll(ctx context.Context, host string, port int, community string) *SNMPInfo {
	if port <= 0 {
		port = 161
	}
	g := &gosnmp.GoSNMP{
		Target:    host,
		Port:      uint16(port),
		Community: community,
		Version:   gosnmp.Version2c,
		Timeout:   2 * time.Second,
		Retries:   1,
		MaxOids:   gosnmp.MaxOids,
		Context:   ctx,
	}
	if err := g.Connect(); err != nil {
		return &SNMPInfo{Error: err.Error(), Polled: time.Now()}
	}
	defer g.Conn.Close()

	info := &SNMPInfo{Polled: time.Now()}

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
	res, err := g.Get([]string{oidUPSBatteryStatus, oidUPSSecondsOnBatt, oidUPSChargeRemain})
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
		case oidUPSChargeRemain:
			u.ChargePercent = n
		}
	}
	if !answered {
		return nil
	}
	return u
}

// walk reads a table, bounded so an unexpected device cannot hold the poll open.
func walk(g *gosnmp.GoSNMP, root string) ([]gosnmp.SnmpPDU, error) {
	var out []gosnmp.SnmpPDU
	err := g.BulkWalk(root, func(pdu gosnmp.SnmpPDU) error {
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

// snmpError shortens the library's errors to something a card can show.
func snmpError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return "no SNMP response (check the community string and that SNMP is enabled)"
	}
	return msg
}
