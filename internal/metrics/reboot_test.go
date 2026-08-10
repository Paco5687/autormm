package metrics

import "testing"

// A host with nothing pending must report nothing, on every platform: a chip
// that is always on is worse than no chip, because it trains people to ignore
// it. (Both Windows hosts in a real fleet showed "reboot pending" immediately
// after being rebooted, which is what prompted this.)
func TestRebootPendingReportsAReasonOrNothing(t *testing.T) {
	pending, reason := rebootPending()
	if pending && reason == "" {
		t.Error("a pending reboot was reported with no explanation of what is asking")
	}
	if !pending && reason != "" {
		t.Errorf("no reboot is pending but a reason was given: %q", reason)
	}
	t.Logf("this host: pending=%v reason=%q", pending, reason)
}
