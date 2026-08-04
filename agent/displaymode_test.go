package agent

import (
	"errors"
	"testing"
)

type applied struct{ index, w, h int }

func recorder(errFor map[int]error) (func(int, int, int) error, *[]applied) {
	var got []applied
	return func(index, w, h int) error {
		got = append(got, applied{index, w, h})
		return errFor[index]
	}, &got
}

// The mode from before the session's *first* change is what gets restored —
// later changes are the operator still moving around, and going back to an
// intermediate size would be no better than leaving it.
func TestRestoresTheModeFromBeforeTheFirstChange(t *testing.T) {
	m := newDisplayModeMemory()
	m.remember(0, 3840, 2160) // the real original
	m.remember(0, 1920, 1080) // operator changed again mid-session
	m.remember(0, 1280, 720)

	apply, got := recorder(nil)
	if errs := m.restore(apply); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(*got) != 1 || (*got)[0] != (applied{0, 3840, 2160}) {
		t.Fatalf("restored %+v, want a single {0 3840 2160}", *got)
	}
}

// A session that never changed anything must not touch the host on the way out.
func TestUntouchedSessionRestoresNothing(t *testing.T) {
	m := newDisplayModeMemory()
	if m.changed() {
		t.Error("changed() true before anything was remembered")
	}
	apply, got := recorder(nil)
	if errs := m.restore(apply); errs != nil {
		t.Errorf("errors from an empty restore: %v", errs)
	}
	if len(*got) != 0 {
		t.Errorf("restored %+v on a session that changed nothing", *got)
	}
}

func TestRestoresEveryChangedDisplay(t *testing.T) {
	m := newDisplayModeMemory()
	m.remember(0, 3840, 2160)
	m.remember(2, 1920, 1200)
	if !m.changed() {
		t.Fatal("changed() false after remembering")
	}

	apply, got := recorder(nil)
	m.restore(apply)
	if len(*got) != 2 {
		t.Fatalf("restored %d displays, want 2", len(*got))
	}
	seen := map[int][2]int{}
	for _, a := range *got {
		seen[a.index] = [2]int{a.w, a.h}
	}
	if seen[0] != [2]int{3840, 2160} || seen[2] != [2]int{1920, 1200} {
		t.Errorf("restored the wrong modes: %v", seen)
	}
}

// One display failing must not strand the others.
func TestRestoreContinuesPastAFailure(t *testing.T) {
	m := newDisplayModeMemory()
	m.remember(0, 3840, 2160)
	m.remember(1, 2560, 1440)

	boom := errors.New("mode rejected")
	apply, got := recorder(map[int]error{0: boom})
	errs := m.restore(apply)
	if len(*got) != 2 {
		t.Errorf("attempted %d displays, want 2", len(*got))
	}
	if !errors.Is(errs[0], boom) {
		t.Errorf("failure for display 0 not reported: %v", errs)
	}
	if _, bad := errs[1]; bad {
		t.Errorf("display 1 reported an error it did not have")
	}
}

// Restoring twice (a defer plus an explicit call, say) must not re-apply.
func TestRestoreIsIdempotent(t *testing.T) {
	m := newDisplayModeMemory()
	m.remember(0, 3840, 2160)

	apply, got := recorder(nil)
	m.restore(apply)
	m.restore(apply)
	if len(*got) != 1 {
		t.Errorf("applied %d times, want 1", len(*got))
	}
	if m.changed() {
		t.Error("still reports changes after restoring")
	}
}

// A display we could not read a size for must not be "restored" to 0x0.
func TestIgnoresUnusableModes(t *testing.T) {
	m := newDisplayModeMemory()
	m.remember(0, 0, 0)
	m.remember(1, 1920, 0)
	if m.changed() {
		t.Error("remembered a mode with no usable size")
	}
}
