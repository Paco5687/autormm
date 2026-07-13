package capture

import (
	"errors"

	"github.com/Paco5687/autormm/internal/protocol"
)

var errUnsupportedRes = errors.New("resolution change not supported on this host")

// DisplayModes returns the selectable resolutions for display index, sorted
// large-to-small and de-duplicated. Empty if unsupported on this platform.
func DisplayModes(index int) []protocol.Mode { return displayModes(index) }

// SetDisplayMode changes display index to w×h. Returns an error if the mode is
// unavailable or resolution changes aren't supported here.
func SetDisplayMode(index, w, h int) error { return setDisplayMode(index, w, h) }

// dedupeSortModes removes duplicates and sorts by area descending.
func dedupeSortModes(in []protocol.Mode) []protocol.Mode {
	seen := map[[2]int]bool{}
	out := make([]protocol.Mode, 0, len(in))
	for _, m := range in {
		if m.W <= 0 || m.H <= 0 {
			continue
		}
		k := [2]int{m.W, m.H}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	// simple insertion sort by area desc (lists are small)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].W*out[j].H > out[j-1].W*out[j-1].H; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
