//go:build race

package capture

// Timing measurements are meaningless under the race detector: it instruments
// every memory access, so wall-clock numbers reflect the tooling rather than
// the pipeline. The latency test runs in its own unraced CI step instead.
const raceEnabled = true
