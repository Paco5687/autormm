package metrics

import (
	"strings"

	"github.com/shirou/gopsutil/v3/host"
)

// cpuTempFrom picks the CPU package temperature out of a host's sensor list.
//
// This needs choosing, not averaging. A real machine reports a dozen sensors —
// NVMe drives, the network adapter, motherboard, VRM — and mixing them produces
// a number that is not any component's temperature. Worse, boards expose
// unconnected headers that read absurd values (a live example reads -62°C), so
// implausible readings must be discarded rather than believed.
//
// The order below is by trustworthiness: each vendor's canonical package sensor
// first, then a core-level maximum, then the ACPI thermal zone as a last resort.
func cpuTempFrom(temps []host.TemperatureStat) float64 {
	// Exact-ish package sensors, best first.
	prefixes := []string{
		"coretemp_package", // Intel package
		"k10temp_tctl",     // AMD control temperature — what AMD tools show
		"k10temp_tdie",     // AMD die
		"cpu_thermal",      // Raspberry Pi and friends
		"cpu_package",      // embedded controllers reporting a package figure
		"zenpower_tdie",
	}
	for _, p := range prefixes {
		if v, ok := bestMatch(temps, p); ok {
			return v
		}
	}
	// No package sensor: the hottest individual core is the honest summary.
	if v, ok := bestMatch(temps, "coretemp_core"); ok {
		return v
	}
	// Last resort. acpitz is often the case rather than the CPU, so it is only
	// used when nothing better exists.
	if v, ok := bestMatch(temps, "acpitz"); ok {
		return v
	}
	return 0
}

// bestMatch returns the highest plausible reading among sensors whose key
// starts with prefix.
func bestMatch(temps []host.TemperatureStat, prefix string) (float64, bool) {
	best, found := 0.0, false
	for _, t := range temps {
		if !strings.HasPrefix(strings.ToLower(t.SensorKey), prefix) {
			continue
		}
		if !plausibleTemp(t.Temperature) {
			continue
		}
		if t.Temperature > best {
			best, found = t.Temperature, true
		}
	}
	return best, found
}

// plausibleTemp rejects readings no running CPU produces. Disconnected headers
// report large negatives, and a sensor that has never been read reports zero;
// believing either would put nonsense on the dashboard.
func plausibleTemp(c float64) bool { return c > 5 && c < 150 }

// collectCPUTemp reads the host's sensors. Absent on hosts with no readable
// sensors at all, which includes most VMs and much of Windows.
func collectCPUTemp() float64 {
	temps, err := host.SensorsTemperatures()
	if err != nil && len(temps) == 0 {
		return 0
	}
	return round1(cpuTempFrom(temps))
}
