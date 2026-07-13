// Package metrics collects a cross-platform host snapshot using gopsutil.
package metrics

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Collector holds the state needed to compute rates between samples.
type Collector struct {
	lastNetTime time.Time
	lastRecv    uint64
	lastSent    uint64
	cpuPrimed   bool
	maxProcs    int
}

// New returns a collector. topN caps the number of processes reported.
func New(topN int) *Collector {
	if topN <= 0 {
		topN = 8
	}
	return &Collector{maxProcs: topN}
}

// HostInfo returns static host identity used at registration time.
func HostInfo() (hostname, os, platform, arch string) {
	if info, err := host.Info(); err == nil {
		return info.Hostname, info.OS, info.Platform + " " + info.PlatformVersion, info.KernelArch
	}
	return "unknown", "", "", ""
}

// Collect gathers a point-in-time snapshot. Individual failures are tolerated
// so a partial snapshot is always returned.
func (c *Collector) Collect() *protocol.Metrics {
	now := time.Now()
	m := &protocol.Metrics{Timestamp: now}

	if up, err := host.Uptime(); err == nil {
		m.UptimeSecs = up
	}

	// CPU. First call primes the baseline and returns 0.
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		if c.cpuPrimed {
			m.CPUPercent = round1(pcts[0])
		}
		c.cpuPrimed = true
	}
	if n, err := cpu.Counts(true); err == nil {
		m.CPUCores = n
	}

	if l, err := load.Avg(); err == nil && l != nil {
		m.Load1, m.Load5, m.Load15 = round2(l.Load1), round2(l.Load5), round2(l.Load15)
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		m.MemTotal, m.MemUsed, m.MemPercent = vm.Total, vm.Used, round1(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		m.SwapTotal, m.SwapUsed = sw.Total, sw.Used
	}

	m.Disks = collectDisks()
	c.collectNet(m, now)
	m.Procs = c.collectProcs()

	if users, err := host.Users(); err == nil {
		seen := map[string]bool{}
		for _, u := range users {
			if u.User != "" && !seen[u.User] {
				seen[u.User] = true
				m.Users = append(m.Users, u.User)
			}
		}
	}
	return m
}

func collectDisks() []protocol.Disk {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	var out []protocol.Disk
	seen := map[string]bool{}
	for _, p := range parts {
		if seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u == nil || u.Total == 0 {
			continue
		}
		out = append(out, protocol.Disk{
			Mount:   p.Mountpoint,
			FSType:  p.Fstype,
			Total:   u.Total,
			Used:    u.Used,
			Percent: round1(u.UsedPercent),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func (c *Collector) collectNet(m *protocol.Metrics, now time.Time) {
	counters, err := net.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return
	}
	recv, sent := counters[0].BytesRecv, counters[0].BytesSent
	if !c.lastNetTime.IsZero() {
		dt := now.Sub(c.lastNetTime).Seconds()
		if dt > 0 {
			if recv >= c.lastRecv {
				m.NetRecv = uint64(float64(recv-c.lastRecv) / dt)
			}
			if sent >= c.lastSent {
				m.NetSent = uint64(float64(sent-c.lastSent) / dt)
			}
		}
	}
	c.lastNetTime, c.lastRecv, c.lastSent = now, recv, sent
}

func (c *Collector) collectProcs() []protocol.ProcInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	var out []protocol.ProcInfo
	for _, p := range procs {
		cpuPct, _ := p.CPUPercent()
		name, _ := p.Name()
		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		if name == "" {
			continue
		}
		out = append(out, protocol.ProcInfo{PID: p.Pid, Name: name, CPU: round1(cpuPct), MemRSS: rss})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		return out[i].MemRSS > out[j].MemRSS
	})
	if len(out) > c.maxProcs {
		out = out[:c.maxProcs]
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
