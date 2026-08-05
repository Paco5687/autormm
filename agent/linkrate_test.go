package agent

import (
	"testing"
	"time"
)

// feed drives the estimator through n one-second windows, each carrying bps
// bits and spending busy of that second blocked in a write.
func feed(l *linkEstimator, n int, bps float64, busy time.Duration, t0 time.Time) time.Time {
	now := t0
	for i := 0; i < n; i++ {
		now = now.Add(time.Second)
		l.observe(int(bps/8), busy, now)
	}
	return now
}

// When writes are blocking, the throughput achieved *is* the link's capacity,
// so the estimate must come down to it rather than sitting on a hopeful number
// whose excess just queues.
func TestEstimateFallsToMeasuredCapacityWhenBacklogged(t *testing.T) {
	l := newLinkEstimator(12_000_000)
	t0 := time.Now()

	// The link really carries 4 Mbps and every write is waiting on it.
	feed(l, 20, 4_000_000, 600*time.Millisecond, t0)

	if got := l.rate(); got > 4_200_000 || got < 3_000_000 {
		t.Errorf("estimate settled at %d bps, want it near the measured 4 Mbps", got)
	}
}

// A quiet desktop sends almost nothing, which says nothing about capacity. The
// estimate must not read that as a slow link.
func TestIdleDesktopDoesNotLowerTheEstimate(t *testing.T) {
	l := newLinkEstimator(8_000_000)
	before := l.rate()

	// Twenty seconds of a nearly-still screen: a trickle of bytes, never blocked.
	feed(l, 20, 50_000, 0, time.Now())

	if got := l.rate(); got < before {
		t.Errorf("estimate fell from %d to %d on an idle desktop; low traffic is not a slow link", before, got)
	}
}

// The failure mode that makes naive versions of this useless: lowering the
// estimate makes the encoder send less, so the next window measures less, so
// the estimate falls again — all the way to the floor, on a link that was
// never slow. Only a backlogged window may lower it, and backing off ends the
// backlog, so it has to converge instead.
func TestEstimateDoesNotSpiralToTheFloor(t *testing.T) {
	l := newLinkEstimator(10_000_000)
	t0 := time.Now()

	// A few genuinely congested seconds. (The first observation only opens the
	// first window, so a measurement needs more than one.)
	t0 = feed(l, 4, 3_000_000, 800*time.Millisecond, t0)
	dropped := l.rate()
	if dropped >= 10_000_000 {
		t.Fatalf("a congested window did not lower the estimate (%d bps)", dropped)
	}

	// ...then the encoder obeys and sends less, unblocked. This is exactly the
	// input that would feed a spiral.
	feed(l, 30, 500_000, 0, t0)

	if got := l.rate(); got <= dropped {
		t.Errorf("estimate went from %d to %d while nothing was blocking: it is chasing its own back-off", dropped, got)
	}
	if got := l.rate(); got == minRateBps {
		t.Errorf("estimate collapsed to the floor on a link that was never slow")
	}
}

// Recovery matters as much as back-off: a link that was briefly congested must
// win its bitrate back, or one bad moment costs quality for the whole session.
func TestEstimateRecoversAfterCongestionClears(t *testing.T) {
	l := newLinkEstimator(12_000_000)
	t0 := time.Now()

	t0 = feed(l, 10, 2_000_000, 700*time.Millisecond, t0) // congested
	low := l.rate()
	feed(l, 60, 2_000_000, 0, t0) // clear, and the encoder is not saturating

	if got := l.rate(); got <= low*2 {
		t.Errorf("estimate only reached %d bps from %d after a minute clear; recovery is too slow", got, low)
	}
}

func TestEstimateStaysWithinBounds(t *testing.T) {
	high := newLinkEstimator(maxRateBps)
	feed(high, 120, 1_000_000, 0, time.Now()) // probe upward for two minutes
	if got := high.rate(); got > maxRateBps {
		t.Errorf("estimate ran past the ceiling: %d", got)
	}

	low := newLinkEstimator(minRateBps)
	feed(low, 60, 10_000, 900*time.Millisecond, time.Now()) // hopelessly congested
	if got := low.rate(); got < minRateBps {
		t.Errorf("estimate fell through the floor: %d", got)
	}
}

// A closed-loop simulation: the encoder spends whatever ceiling it is given,
// the link delivers what it can, and writes block when it cannot keep up. This
// is the question the unit tests above cannot answer on their own — does the
// control law actually settle near the truth, from either side of it?
func simulate(t *testing.T, capacityBps, startBps float64, windows int) (settled float64, worstOvershoot float64) {
	t.Helper()
	l := newLinkEstimator(startBps)
	now := time.Now()
	for i := 0; i < windows; i++ {
		now = now.Add(time.Second)
		offered := l.est // during motion the encoder spends its whole ceiling

		delivered := offered
		var blocked time.Duration
		if offered > capacityBps {
			// The link is the limit: only capacity gets through, and the rest of
			// the second is spent waiting inside writes.
			delivered = capacityBps
			over := (offered - capacityBps) / offered
			blocked = time.Duration(over * float64(time.Second))
			if blocked > time.Second {
				blocked = time.Second
			}
			if ratio := offered / capacityBps; ratio > worstOvershoot {
				worstOvershoot = ratio
			}
		}
		l.observe(int(delivered/8), blocked, now)
	}
	return l.est, worstOvershoot
}

func TestEstimateConvergesOnAConstrainedLink(t *testing.T) {
	const capacity = 5_000_000

	// Starting far too high (the situation on a slow link: we flood it).
	high, overshoot := simulate(t, capacity, 20_000_000, 90)
	if high > capacity*1.15 || high < capacity*0.5 {
		t.Errorf("from 20Mbps, settled at %.0f bps on a %d bps link", high, capacity)
	}
	t.Logf("from 20Mbps -> %.2f Mbps (worst overshoot %.1fx)", high/1e6, overshoot)

	// Starting far too low (the situation today: a fixed cap well under what the
	// link could carry, which is what makes motion blocky).
	low, _ := simulate(t, capacity, 800_000, 90)
	if low < capacity*0.5 {
		t.Errorf("from 800kbps, only reached %.0f bps on a %d bps link; headroom is left unused", low, capacity)
	}
	t.Logf("from 800kbps -> %.2f Mbps", low/1e6)
}

// The case that motivated all of this: a link with room to spare must actually
// be used, or motion stays blocky for no reason.
func TestEstimateClimbsToUseAFastLink(t *testing.T) {
	got, _ := simulate(t, 20_000_000, startRateBps, 120)
	if got < 12_000_000 {
		t.Errorf("on a 20Mbps link the estimate only reached %.1f Mbps; that is the blockiness complaint unfixed", got/1e6)
	}
	t.Logf("20Mbps link -> %.1f Mbps", got/1e6)
}
