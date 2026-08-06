package metrics

import "testing"

func TestParseNvidiaSMI(t *testing.T) {
	// Real nvidia-smi output shape: --format=csv,noheader,nounits, memory in MiB.
	out := `NVIDIA GeForce RTX 4090, 37, 2048, 24564, 51
NVIDIA GeForce RTX 3090, 0, 512, 24576, 38
`
	g := parseNvidiaSMI(out)
	if len(g) != 2 {
		t.Fatalf("got %d GPUs, want 2", len(g))
	}
	if g[0].Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("name = %q", g[0].Name)
	}
	if g[0].Percent != 37 {
		t.Errorf("utilisation = %v, want 37", g[0].Percent)
	}
	// MiB in, bytes out — the rest of Metrics reports memory in bytes and a
	// dashboard that mixed the two would be quietly wrong by 1048576x.
	if want := uint64(2048) * 1024 * 1024; g[0].MemUsed != want {
		t.Errorf("mem used = %d, want %d bytes", g[0].MemUsed, want)
	}
	if want := uint64(24564) * 1024 * 1024; g[0].MemTotal != want {
		t.Errorf("mem total = %d, want %d bytes", g[0].MemTotal, want)
	}
	if g[0].MemPercent < 8.3 || g[0].MemPercent > 8.4 {
		t.Errorf("mem percent = %v, want ~8.34", g[0].MemPercent)
	}
	if g[1].Percent != 0 || g[1].TempC != 38 {
		t.Errorf("second card parsed wrong: %+v", g[1])
	}
}

// Cards that do not report a value emit "[N/A]" rather than a number, and a
// partial line must not produce a bogus record.
func TestParseNvidiaSMIHandlesMissingValues(t *testing.T) {
	g := parseNvidiaSMI("Tesla T4, [N/A], 100, 15360, [N/A]\n")
	if len(g) != 1 {
		t.Fatalf("got %d GPUs, want 1", len(g))
	}
	if g[0].Percent != 0 || g[0].TempC != 0 {
		t.Errorf("unreported values should read 0, got %+v", g[0])
	}
	if g[0].MemTotal == 0 {
		t.Error("the values that were reported should still be parsed")
	}
}

func TestParseNvidiaSMIIgnoresJunk(t *testing.T) {
	for _, in := range []string{"", "\n\n", "not,enough\n", "  \n"} {
		if g := parseNvidiaSMI(in); len(g) != 0 {
			t.Errorf("parseNvidiaSMI(%q) returned %d GPUs, want none", in, len(g))
		}
	}
}

// A host with no total VRAM reported must not divide by zero.
func TestParseNvidiaSMINoDivideByZero(t *testing.T) {
	g := parseNvidiaSMI("Weird Card, 10, 0, 0, 40\n")
	if len(g) != 1 || g[0].MemPercent != 0 {
		t.Errorf("got %+v, want a single GPU with 0%% memory", g)
	}
}

// The overwhelmingly common case: a host with no NVIDIA tooling at all. It must
// return nothing, quickly, without spawning anything or erroring — this runs on
// every agent in the fleet every few seconds.
func TestCollectGPUsIsSafeWithoutNvidiaSMI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing findable
	if g := collectGPUs(); g != nil {
		t.Errorf("expected no GPUs without nvidia-smi, got %+v", g)
	}
}

// A full Collect must include the field on a GPU host and omit it otherwise,
// and must not blow up either way.
func TestCollectIncludesGPUField(t *testing.T) {
	m := New(3).Collect()
	if m == nil {
		t.Fatal("Collect returned nil")
	}
	for _, g := range m.GPUs {
		if g.MemTotal > 0 && (g.MemPercent < 0 || g.MemPercent > 100) {
			t.Errorf("implausible VRAM percentage: %+v", g)
		}
	}
	t.Logf("this host reports %d GPU(s)", len(m.GPUs))
}
