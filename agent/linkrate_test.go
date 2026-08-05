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

	// Holding is the correct answer here, not climbing: a window spent sending a
	// fraction of the ceiling without blocking carries no information either way.
	// What must never happen is falling further.
	if got := l.rate(); got < dropped {
		t.Errorf("estimate went from %d to %d while nothing was blocking: it is chasing its own back-off", dropped, got)
	}
	if got := l.rate(); got == minRateBps {
		t.Errorf("estimate collapsed to the floor on a link that was never slow")
	}
}

// Recovery matters as much as back-off: a link that was briefly congested must
// win its bitrate back, or one bad moment costs quality for the whole session.
//
// This has to be driven closed-loop. Offering a fixed load instead would prove
// nothing, because once the ceiling rises past what is being offered there is
// no evidence more is available and holding is the right answer.
func TestEstimateRecoversAfterCongestionClears(t *testing.T) {
	l := newLinkEstimator(12_000_000)
	now := time.Now()

	now = runLink(l, 2_000_000, 20, now) // the link degrades
	low := l.rate()
	if low > 3_000_000 {
		t.Fatalf("estimate did not follow the link down: %d", low)
	}

	runLink(l, 15_000_000, 60, now) // and recovers
	if got := l.rate(); got < 8_000_000 {
		t.Errorf("estimate only reached %d bps after the link recovered to 15Mbps", got)
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
	runLink(l, capacityBps, windows, time.Now())
	return l.est, 0
}

// runLink drives an estimator through n one-second windows against a link of a
// given capacity, with the encoder spending whatever ceiling it currently has.
func runLink(l *linkEstimator, capacityBps float64, windows int, start time.Time) time.Time {
	now := start
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
		}
		l.observe(int(delivered/8), blocked, now)
	}
	return now
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

// The estimate must never pin itself at the hard ceiling on evidence it does
// not have.
//
// Reported from a real session: the viewer showed a steady 25 Mbps — the clamp
// — while the framerate sat between 2 and 12. Nothing was ever pushing back, so
// an estimate that climbed on every unblocked window simply ratcheted to the
// top and stayed. The encoder then budgeted 25 Mbps of video per second for a
// link carrying far less, which went out as a few very large frames instead of
// many small ones. That is the framerate collapse, caused by the "measurement"
// rather than merely unnoticed by it.
func TestEstimateDoesNotPinAtTheCeilingWithoutEvidence(t *testing.T) {
	l := newLinkEstimator(startRateBps)
	now := time.Now()

	// Five minutes of a mostly-still desktop: a trickle of bytes, never blocked,
	// which is exactly the case that carries no information about capacity.
	for i := 0; i < 300; i++ {
		now = now.Add(time.Second)
		l.observe(120_000/8, 0, now) // ~120 kbps
	}

	if got := l.rate(); got >= maxRateBps {
		t.Errorf("estimate reached the hard ceiling (%d bps) on an idle desktop that proved nothing", got)
	}
	if got := l.rate(); got > startRateBps*2 {
		t.Errorf("estimate drifted to %d bps while only ~120 kbps was ever sent", got)
	}
}

// The case the sender-side signal cannot see: something in the path buffers, so
// writes never block however far over capacity we push.
//
// This is not hypothetical — a reverse proxy terminating TLS, or a zero-trust
// overlay, sits in exactly that position and will absorb megabytes. Blocked
// writes then report a link far faster than the truth, the estimate ratchets to
// the hard ceiling, and the encoder sizes a stream the path cannot carry. What
// the viewer counts arriving is the one figure none of that can inflate.
func TestReceiverReportBeatsAHidingProxy(t *testing.T) {
	const capacity = 6_000_000
	l := newLinkEstimator(20_000_000)
	now := time.Now()

	for i := 0; i < 40; i++ {
		now = now.Add(time.Second)
		offered := l.est
		// The proxy swallows everything instantly: writes never block, so the
		// sender-side signal is silent no matter how badly we overshoot.
		l.observeReceived(min(offered, capacity))
		l.observe(int(offered/8), 0, now)
	}

	if got := l.rate(); got > capacity*1.3 {
		t.Errorf("estimate stayed at %d bps on a %d bps path; the receiver report was ignored", got, capacity)
	}
	if got := l.rate(); got < capacity*0.5 {
		t.Errorf("estimate collapsed to %d bps on a %d bps path", got, capacity)
	}
	t.Logf("hidden %d bps bottleneck -> estimate %.1f Mbps", capacity, float64(l.rate())/1e6)
}

// A viewer that never reports (an older one) must not be harmed by the feature.
func TestMissingReceiverReportsAreHarmless(t *testing.T) {
	l := newLinkEstimator(startRateBps)
	runLink(l, 8_000_000, 60, time.Now()) // no observeReceived calls at all
	if got := l.rate(); got < 4_000_000 {
		t.Errorf("without receiver reports the estimate stalled at %d bps on an 8Mbps link", got)
	}
}

// The receiver signal must work even when the encoder is nowhere near its
// ceiling — which is exactly when a wrong ceiling does its damage.
//
// A ceiling far above what the path carries makes the encoder emit large frames
// that go out slowly, so it never reaches the "using most of the ceiling" mark.
// Gating the receiver report on that mark therefore disqualified the only
// evidence that could fix the ceiling, using a symptom of the ceiling being
// wrong as the reason to ignore it. The estimate then sat on its starting value
// indefinitely while the picture stayed blocky.
func TestReceiverReportAppliesEvenWhenNotSaturating(t *testing.T) {
	l := newLinkEstimator(startRateBps)
	now := time.Now()

	// The host manages only a trickle — well under the 4Mbps ceiling — and about
	// half of it is arriving. Nothing blocks, because something in the path is
	// absorbing the difference.
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		const sent = 1_400_000.0
		l.observeReceived(sent * 0.5)
		l.observe(int(sent/8), 0, now)
	}

	if got := l.rate(); got >= startRateBps {
		t.Errorf("estimate stuck at its starting value (%d bps) while half of what was sent arrived", got)
	}
	t.Logf("under-saturated, half arriving -> estimate %.2f Mbps", float64(l.rate())/1e6)
}
