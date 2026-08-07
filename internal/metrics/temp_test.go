package metrics

import (
	"testing"

	"github.com/shirou/gopsutil/v3/host"
)

func stats(pairs ...any) []host.TemperatureStat {
	var out []host.TemperatureStat
	for i := 0; i < len(pairs); i += 2 {
		var c float64
		switch v := pairs[i+1].(type) {
		case float64:
			c = v
		case int:
			c = float64(v) // so a plain 0 in a test reads naturally
		}
		out = append(out, host.TemperatureStat{SensorKey: pairs[i].(string), Temperature: c})
	}
	return out
}

// Captured verbatim from a real AMD workstation: fifteen sensors, of which two
// are the NIC, three are NVMe, and one is an unconnected header reading -62°C.
func TestCPUTempFromRealAMDBoard(t *testing.T) {
	got := cpuTempFrom(stats(
		"enp12s0_phy_temperature", 63.0,
		"enp12s0_mac_temperature", 63.0,
		"nvme_composite", 50.9,
		"nvme_sensor_1", 50.9,
		"nvme_sensor_2", 53.9,
		"k10temp_tctl", 52.1,
		"k10temp_tccd1", 33.9,
		"k10temp_tccd2", 33.4,
		"asusec_cpu", 42.0,
		"asusec_cpu_package", 52.0,
		"asusec_motherboard", 27.0,
		"asusec_t_sensor", -62.0,
		"asusec_vrm", 57.0,
	))
	// The NIC is the hottest thing here at 63°C, and the VRM is hotter than the
	// CPU: anything that maximises across all sensors gets this wrong.
	if got != 52.1 {
		t.Errorf("cpu temp = %v, want k10temp_tctl 52.1", got)
	}
}

func TestCPUTempIntelPrefersPackage(t *testing.T) {
	got := cpuTempFrom(stats(
		"coretemp_core_0", 61.0,
		"coretemp_core_1", 64.0,
		"coretemp_package_id_0", 66.0,
		"nvme_composite", 71.0, // hotter, and irrelevant
	))
	if got != 66.0 {
		t.Errorf("cpu temp = %v, want the package sensor 66", got)
	}
}

// No package sensor: the hottest core is the honest summary.
func TestCPUTempFallsBackToHottestCore(t *testing.T) {
	if got := cpuTempFrom(stats("coretemp_core_0", 55.0, "coretemp_core_1", 61.0)); got != 61.0 {
		t.Errorf("cpu temp = %v, want 61", got)
	}
}

// acpitz is frequently the chassis rather than the CPU, so it must lose to any
// real CPU sensor and only be used when nothing else exists.
func TestCPUTempUsesACPIOnlyAsLastResort(t *testing.T) {
	if got := cpuTempFrom(stats("acpitz", 40.0, "k10temp_tctl", 58.0)); got != 58.0 {
		t.Errorf("acpitz beat a real CPU sensor: %v", got)
	}
	if got := cpuTempFrom(stats("acpitz", 40.0)); got != 40.0 {
		t.Errorf("acpitz not used as a fallback: %v", got)
	}
}

// Nonsense must be discarded, not displayed.
func TestCPUTempRejectsImplausibleReadings(t *testing.T) {
	if got := cpuTempFrom(stats("k10temp_tctl", -62.0)); got != 0 {
		t.Errorf("a disconnected header was believed: %v", got)
	}
	if got := cpuTempFrom(stats("coretemp_package_id_0", 0)); got != 0 {
		t.Errorf("an unread sensor was believed: %v", got)
	}
	if got := cpuTempFrom(nil); got != 0 {
		t.Errorf("empty sensor list produced %v", got)
	}
	// A host with only irrelevant sensors reports nothing rather than the NVMe.
	if got := cpuTempFrom(stats("nvme_composite", 50.0, "enp3s0_temp", 60.0)); got != 0 {
		t.Errorf("a non-CPU sensor was reported as the CPU: %v", got)
	}
}
