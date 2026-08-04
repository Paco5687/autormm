package agent

import "sync"

// displayModeMemory remembers the resolution a display had before a session
// changed it, so the host can be put back when the operator disconnects.
//
// Without this, shrinking a 4K host to something that suits a phone leaves it
// that way for whoever is sitting at it. Only the mode from *before* this
// session's first change is kept: later changes within the same session are
// still the operator moving around, and restoring to an intermediate size would
// be no better than leaving it.
type displayModeMemory struct {
	mu   sync.Mutex
	orig map[int][2]int // display index -> {w, h} before we touched it
}

func newDisplayModeMemory() *displayModeMemory {
	return &displayModeMemory{orig: map[int][2]int{}}
}

// remember records a display's pre-change mode. Repeat calls for the same
// display are ignored, so the first one wins.
func (m *displayModeMemory) remember(index, w, h int) {
	if w <= 0 || h <= 0 {
		return // nothing useful to go back to
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.orig[index]; seen {
		return
	}
	m.orig[index] = [2]int{w, h}
}

// changed reports whether this session altered any display.
func (m *displayModeMemory) changed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.orig) > 0
}

// restore puts every remembered display back via apply, and forgets them so a
// second call is a no-op. Failures are reported per display rather than
// abandoning the rest.
func (m *displayModeMemory) restore(apply func(index, w, h int) error) map[int]error {
	m.mu.Lock()
	pending := m.orig
	m.orig = map[int][2]int{}
	m.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}
	errs := map[int]error{}
	for index, wh := range pending {
		if err := apply(index, wh[0], wh[1]); err != nil {
			errs[index] = err
		}
	}
	return errs
}
