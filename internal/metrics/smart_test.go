package metrics

import (
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Shapes taken from real smartctl -j output (7.x).
const ataJSON = `{
  "model_name": "WDC WD80EFAX-68KNBN0",
  "smart_status": {"passed": true},
  "temperature": {"current": 34},
  "power_on_time": {"hours": 21341},
  "ata_smart_attributes": {"table": [
    {"id": 5,   "name": "Reallocated_Sector_Ct",  "raw": {"value": 0}},
    {"id": 194, "name": "Temperature_Celsius",    "raw": {"value": 34}},
    {"id": 197, "name": "Current_Pending_Sector", "raw": {"value": 8}},
    {"id": 198, "name": "Offline_Uncorrectable",  "raw": {"value": 0}}
  ]}
}`

const nvmeJSON = `{
  "model_name": "Samsung SSD 980 PRO 2TB",
  "smart_status": {"passed": true, "nvme": {"value": 0}},
  "temperature": {"current": 45},
  "power_on_time": {"hours": 803},
  "nvme_smart_health_information_log": {
    "critical_warning": 0, "percentage_used": 3,
    "media_errors": 0, "unsafe_shutdowns": 12
  }
}`

// What smartctl emits without privilege, for an unsupported bridge, or for a
// drive in standby: a valid JSON envelope with no smart_status.
const noDataJSON = `{
  "smartctl": {"exit_status": 2, "messages": [
    {"string": "Smartctl open device: /dev/sda failed: Permission denied", "severity": "error"}
  ]}
}`

func TestParseSmartATA(t *testing.T) {
	d, ok := parseSmartDevice("/dev/sda", []byte(ataJSON))
	if !ok {
		t.Fatal("real ATA output rejected")
	}
	if d.Model != "WDC WD80EFAX-68KNBN0" || !d.Passed || d.TempC != 34 || d.PowerOnHours != 21341 {
		t.Errorf("basics parsed wrong: %+v", d)
	}
	if d.Pending != 8 || d.Reallocated != 0 || d.Uncorrectable != 0 {
		t.Errorf("attributes parsed wrong: %+v", d)
	}
	// Eight pending sectors with a PASSED verdict is exactly the drive that is
	// about to die politely. The judgment must not trust the verdict.
	if d.Healthy() {
		t.Error("a drive with pending sectors was judged healthy because its firmware said PASSED")
	}
}

func TestParseSmartNVMe(t *testing.T) {
	d, ok := parseSmartDevice("/dev/nvme0", []byte(nvmeJSON))
	if !ok {
		t.Fatal("real NVMe output rejected")
	}
	if !d.Passed || d.PercentUsed != 3 || d.MediaErrors != 0 || d.CriticalWarn != 0 {
		t.Errorf("NVMe fields parsed wrong: %+v", d)
	}
	if !d.Healthy() {
		t.Error("a clean NVMe drive was judged unhealthy")
	}
}

func TestParseSmartNoData(t *testing.T) {
	for name, in := range map[string]string{
		"permission denied": noDataJSON,
		"empty":             "",
		"junk":              "not json at all",
	} {
		if _, ok := parseSmartDevice("/dev/sda", []byte(in)); ok {
			t.Errorf("%s produced a drive record from nothing", name)
		}
	}
}

// A drive in standby yields no reading this pass but is still listed by the
// scan; its previous reading must survive rather than flapping out of the UI
// every ten minutes overnight.
func TestStandbyDriveKeepsItsLastReading(t *testing.T) {
	prev := []protocol.SmartDisk{{Device: "/dev/sdb", Model: "old reading", Passed: true}}
	if got, ok := lookupSmart(prev, "/dev/sdb"); !ok || got.Model != "old reading" {
		t.Errorf("previous reading not found: %+v ok=%v", got, ok)
	}
	if _, ok := lookupSmart(prev, "/dev/sdc"); ok {
		t.Error("a reading was invented for a device that never had one")
	}
}

func TestCollectSMARTSafeWithoutTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	smartMu.Lock()
	smartAt = time.Time{} // defeat the cache so LookPath actually runs
	smartLast = nil
	smartMu.Unlock()
	if got := collectSMART(); got != nil {
		t.Errorf("expected nothing without smartctl, got %+v", got)
	}
}
