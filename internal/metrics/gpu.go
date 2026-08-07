package metrics

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/procattr"
	"github.com/Paco5687/autormm/internal/protocol"
)

// collectGPUs reports NVIDIA GPU utilisation and VRAM, or nil on a host without
// one.
//
// Shelling out to nvidia-smi rather than linking NVML keeps these binaries
// CGO-free and dependency-free, which is the whole reason they cross-compile to
// five targets from one machine. The cost is a process spawn per sample; at the
// default five-second interval that is affordable, and the timeout below bounds
// what a wedged driver can do to the metrics loop.
//
// A host without the tool costs nothing at all: LookPath fails and nothing is
// spawned. That result is deliberately not cached, so a machine that gains a GPU
// or has its driver installed later starts reporting on the next sample instead
// of needing the agent restarted.
func collectGPUs() []protocol.GPU {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu",
		"--format=csv,noheader,nounits")
	// Without this the tray agent, which has no console of its own, gives this
	// one a real console window — appearing and vanishing on the user's desktop
	// every sampling interval.
	procattr.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(string(out))
}

// parseNvidiaSMI turns the CSV nvidia-smi emits into GPU records, one per card.
//
// Split out from the exec so it can be tested without a GPU present — which is
// every machine this is built on.
func parseNvidiaSMI(out string) []protocol.GPU {
	var gpus []protocol.GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		num := func(i int) float64 {
			if i >= len(f) {
				return 0
			}
			// "[N/A]" appears for values a particular card does not report;
			// ParseFloat rejects it and 0 is the right answer.
			v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v
		}
		// nounits reports memory in MiB.
		const mib = 1024 * 1024
		used, total := uint64(num(2))*mib, uint64(num(3))*mib
		g := protocol.GPU{
			Name:     strings.TrimSpace(f[0]),
			Percent:  round1(num(1)),
			MemUsed:  used,
			MemTotal: total,
			TempC:    round1(num(4)),
		}
		if total > 0 {
			g.MemPercent = round1(float64(used) / float64(total) * 100)
		}
		gpus = append(gpus, g)
	}
	return gpus
}
