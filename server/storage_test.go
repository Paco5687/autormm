package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestHistoryInsertQueryPrune(t *testing.T) {
	h, err := OpenHistory(filepath.Join(t.TempDir(), "hist.db"), time.Hour)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer h.Close()

	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 10; i++ {
		h.Insert("h1", &protocol.Metrics{
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
			CPUPercent: float64(i * 10),
			MemPercent: 50,
			Disks:      []protocol.Disk{{Mount: "/", Percent: float64(i)}},
		})
	}
	// A different host should not leak into h1's results.
	h.Insert("h2", &protocol.Metrics{Timestamp: base, CPUPercent: 999})

	pts, err := h.Query("h1", base.Add(-time.Minute), time.Now(), 120)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(pts) == 0 {
		t.Fatal("expected points, got none")
	}
	for _, p := range pts {
		if p.CPU > 100 {
			t.Fatalf("h2 data leaked into h1: cpu=%v", p.CPU)
		}
	}
	if pts[len(pts)-1].DiskMax < pts[0].DiskMax {
		t.Fatalf("disk_max should rise over time: %v -> %v", pts[0].DiskMax, pts[len(pts)-1].DiskMax)
	}

	// Old sample gets pruned.
	h.Insert("h1", &protocol.Metrics{Timestamp: time.Now().Add(-2 * time.Hour), CPUPercent: 5})
	h.prune()
	old, _ := h.Query("h1", time.Now().Add(-3*time.Hour), time.Now().Add(-90*time.Minute), 10)
	if len(old) != 0 {
		t.Fatalf("expected pruned rows to be gone, got %d", len(old))
	}
}

func TestParseRange(t *testing.T) {
	cases := map[string]time.Duration{
		"":     time.Hour,
		"30m":  30 * time.Minute,
		"6h":   6 * time.Hour,
		"7d":   7 * 24 * time.Hour,
		"junk": time.Hour,
	}
	for in, want := range cases {
		if got := parseRange(in, time.Hour); got != want {
			t.Errorf("parseRange(%q) = %v, want %v", in, got, want)
		}
	}
}
