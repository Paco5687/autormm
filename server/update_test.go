package server

import "testing"

// The comparison that decides whether to replace the running hub.
//
// It used to be `latest != Version`, and the direction it got wrong is the one
// that costs you something: on 2026-10-24 a hub built by hand as 0.7.12, ahead
// of the 0.7.11 then published, was offered an "update" that would have
// replaced it with the older release and taken two fixes with it.
func TestUpdateAvailableNeverOffersAnOlderRelease(t *testing.T) {
	cases := []struct {
		latest, current  string
		available, ahead bool
		why              string
	}{
		{"0.7.13", "0.7.12", true, false, "an ordinary upgrade"},
		{"0.7.12", "0.7.12", false, false, "already on it"},
		{"0.7.11", "0.7.12", false, true, "the case that started this: the hub is ahead"},
		{"1.0.0", "0.9.9", true, false, "a major bump is still just newer"},
		{"0.8.0", "0.7.99", true, false, "compared as numbers, not as text"},
		{"0.7.9", "0.7.10", false, true, "0.7.10 beats 0.7.9; string order would disagree"},
		{"0.7.12", "dev", false, false, "an unstamped build cannot be placed in the order"},
		{"", "0.7.12", false, false, "no release to compare against"},
		{"0.7.12", "0.7.12-hand", false, false, "a hand-made version is not comparable"},
		{"v0.7.13", "0.7.12", true, false, "a leading v is tolerated on either side"},
	}
	for _, c := range cases {
		available, ahead := updateAvailable(c.latest, c.current)
		if available != c.available || ahead != c.ahead {
			t.Errorf("updateAvailable(%q, %q) = (%v, %v), want (%v, %v) — %s",
				c.latest, c.current, available, ahead, c.available, c.ahead, c.why)
		}
	}
}

// Text ordering is the trap this replaces: "0.7.9" > "0.7.10" as strings, so a
// release crossing a ten boundary would look like a downgrade.
func TestCompareVersionsOrdersNumerically(t *testing.T) {
	if cmp, ok := compareVersions("0.7.10", "0.7.9"); !ok || cmp != 1 {
		t.Errorf("compareVersions(0.7.10, 0.7.9) = (%d, %v), want (1, true)", cmp, ok)
	}
	if cmp, ok := compareVersions("0.7", "0.7.0"); !ok || cmp != 0 {
		t.Errorf("a missing component should read as zero: got (%d, %v)", cmp, ok)
	}
	for _, v := range []string{"dev", "", "0.7.x", "0.7.12-rc1", "latest"} {
		if _, ok := compareVersions("0.7.12", v); ok {
			t.Errorf("compareVersions treated %q as comparable", v)
		}
	}
}
