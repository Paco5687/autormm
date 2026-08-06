package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// Most hosts have no GPU, and they must not carry an empty field for one. This
// is what makes the dashboard's GPU rows appear only where they mean something,
// rather than showing 0% on every machine in the fleet.
func TestGPUsAreAbsentFromHostsWithoutOne(t *testing.T) {
	b, err := json.Marshal(Metrics{CPUPercent: 12})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "gpus") {
		t.Errorf("a host with no GPU still sent a gpus field: %s", b)
	}
}

func TestGPUsSurviveTheRoundTrip(t *testing.T) {
	const gb = 1024 * 1024 * 1024
	in := Metrics{GPUs: []GPU{{
		Name: "NVIDIA GeForce RTX 4090", Percent: 42,
		MemUsed: 8 * gb, MemTotal: 24 * gb, MemPercent: 33.3, TempC: 61,
	}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Metrics
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.GPUs) != 1 {
		t.Fatalf("got %d GPUs back, want 1", len(out.GPUs))
	}
	g := out.GPUs[0]
	if g.Name != in.GPUs[0].Name || g.Percent != 42 || g.MemUsed != 8*gb || g.MemTotal != 24*gb {
		t.Errorf("round trip changed the reading: %+v", g)
	}
}
