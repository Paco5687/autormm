package metrics

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/procattr"
	"github.com/Paco5687/autormm/internal/protocol"
)

// S.M.A.R.T. drive health, via smartctl — the same shape as the GPU probe:
// shell out if the tool is present, report nothing otherwise, never cache the
// negative so installing smartmontools later just works.
//
// Two things make this one different. Drive health changes on the scale of
// hours, so it is sampled every ten minutes and the cached result rides along
// with every metrics payload in between — polling at the 5s metrics interval
// would be pure waste. And a sleeping disk must stay asleep: a homelab NAS
// spins drives down on purpose, and a monitoring tool that wakes every drive
// every few minutes is destroying the thing it watches, so every query carries
// -n standby and a skipped drive keeps its previous reading.
const smartInterval = 10 * time.Minute

var (
	smartMu   sync.Mutex
	smartAt   time.Time
	smartLast []protocol.SmartDisk
)

func collectSMART() []protocol.SmartDisk {
	smartMu.Lock()
	defer smartMu.Unlock()
	if time.Since(smartAt) < smartInterval {
		return smartLast
	}
	smartAt = time.Now()

	bin, err := exec.LookPath("smartctl")
	if err != nil {
		smartLast = nil
		return nil
	}
	smartLast = querySmart(bin, smartLast)
	return smartLast
}

// querySmart scans for drives and queries each one. prev supplies the reading
// to carry forward for a drive the scan still lists but that returned no data
// this pass — a drive in standby, which must be left asleep rather than woken
// for a health check. A drive the scan no longer lists is simply gone.
func querySmart(bin string, prev []protocol.SmartDisk) []protocol.SmartDisk {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scan := exec.CommandContext(ctx, bin, "--scan", "-j")
	procattr.Hide(scan)
	out, _ := scan.Output()
	var sc struct {
		Devices []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"devices"`
	}
	if json.Unmarshal(out, &sc) != nil || len(sc.Devices) == 0 {
		return nil
	}

	var disks []protocol.SmartDisk
	for _, d := range sc.Devices {
		cmd := exec.CommandContext(ctx, bin, "-j", "-H", "-A", "-n", "standby", "-d", d.Type, d.Name)
		procattr.Hide(cmd)
		// smartctl's exit status is a bitmask — bit 3 set means "disk failing",
		// which is precisely the report we are here for. Output() returning an
		// error therefore must not discard the JSON that came with it.
		out, _ := cmd.Output()
		if disk, ok := parseSmartDevice(d.Name, out); ok {
			disks = append(disks, disk)
		} else if old, ok := lookupSmart(prev, d.Name); ok {
			disks = append(disks, old)
		}
	}
	return disks
}

// lookupSmart finds a previous reading for a device.
func lookupSmart(prev []protocol.SmartDisk, dev string) (protocol.SmartDisk, bool) {
	for _, p := range prev {
		if p.Device == dev {
			return p, true
		}
	}
	return protocol.SmartDisk{}, false
}

// parseSmartDevice turns one smartctl -j report into a SmartDisk. ok is false
// when the report carries no health data — permission denied, an unsupported
// bridge, or a drive in standby.
func parseSmartDevice(name string, out []byte) (protocol.SmartDisk, bool) {
	var r struct {
		ModelName   string `json:"model_name"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int64 `json:"hours"`
		} `json:"power_on_time"`
		ATA struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		NVMe *struct {
			CriticalWarning int   `json:"critical_warning"`
			PercentageUsed  int   `json:"percentage_used"`
			MediaErrors     int64 `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
	}
	if json.Unmarshal(out, &r) != nil || r.SmartStatus == nil {
		return protocol.SmartDisk{}, false
	}
	d := protocol.SmartDisk{
		Device:       name,
		Model:        r.ModelName,
		Passed:       r.SmartStatus.Passed,
		TempC:        r.Temperature.Current,
		PowerOnHours: r.PowerOnTime.Hours,
	}
	for _, a := range r.ATA.Table {
		switch a.ID {
		case 5:
			d.Reallocated = a.Raw.Value
		case 197:
			d.Pending = a.Raw.Value
		case 198:
			d.Uncorrectable = a.Raw.Value
		}
	}
	if r.NVMe != nil {
		d.CriticalWarn = r.NVMe.CriticalWarning
		d.PercentUsed = r.NVMe.PercentageUsed
		d.MediaErrors = r.NVMe.MediaErrors
	}
	return d, true
}
