package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Bounds on the estimate. The floor keeps a session legible on a bad link
// rather than letting it collapse to nothing; the ceiling is well above any
// plausible desktop need, so it only stops a runaway probe.
const (
	minRateBps = 600_000
	maxRateBps = 25_000_000
)

// backloggedFrac is the share of a window spent inside blocked writes that
// counts as "the link is the limit".
//
// Any probing loop must overshoot to find the limit at all; the question is by
// how much, since everything above capacity is queued and queuing is latency.
// Simulated across 5Mbps and 20Mbps links, 0.15 settled 6% above capacity with
// peaks of 1.22x, while 0.10 settles on capacity with peaks of 1.13x — one
// window's worth, which the VBV buffer absorbs.
const backloggedFrac = 0.10

// probeWhenUsing is the share of the current ceiling that must actually be in
// use before reaching for more. Without it the estimate climbs on windows that
// contain no information — an idle desktop — and ends up pinned at the hard
// ceiling, which is a guess wearing a measurement's clothes.
const probeWhenUsing = 0.7

// linkEstimator works out how much the viewer's connection actually carries.
//
// This exists because the bitrate ceiling used to be a number picked by hand,
// and a hand-picked number is wrong nearly everywhere. Measured on a window
// drag at 1680x1050, the content wants ~21 Mbps; the fixed 3.6 Mbps cap gave up
// 6.4 dB of quality, which is what "blocky during motion" looks like. On a
// slower link the same fixed number is too *high* and the excess simply queues,
// which is latency. Only measurement fits both.
//
// The signal is the media socket itself. The hub relays with a synchronous
// read-then-write loop, so when the viewer's link is full its socket blocks,
// which stops the hub reading, which pushes back through TCP to the agent. Time
// spent inside a blocked write is therefore a direct report on the far end.
type linkEstimator struct {
	mu sync.Mutex

	window     time.Duration
	backlogged float64
	est        float64

	// rx is the rate the viewer reports actually receiving, smoothed. Zero until
	// the first report arrives (older viewers never send one).
	rx float64

	bytes     int
	blocked   time.Duration
	windowEnd time.Time
	started   bool
}

func newLinkEstimator(start float64) *linkEstimator {
	return &linkEstimator{window: time.Second, backlogged: backloggedFrac, est: clampRate(start)}
}

// observe records one completed write: how much was sent and how long the call
// took. A write that returns immediately spent no time waiting on the network.
func (l *linkEstimator) observe(n int, took time.Duration, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.started {
		l.started, l.windowEnd = true, now.Add(l.window)
	}
	l.bytes += n
	l.blocked += took
	if now.Before(l.windowEnd) {
		return
	}
	l.roll(now)
}

// roll closes the measurement window and moves the estimate. The caller holds
// l.mu.
func (l *linkEstimator) roll(now time.Time) {
	elapsed := l.window + now.Sub(l.windowEnd)
	if elapsed <= 0 {
		elapsed = l.window
	}
	secs := elapsed.Seconds()
	goodput := float64(l.bytes) * 8 / secs
	busy := l.blocked.Seconds() / secs

	// Backlogged: the writes were waiting on the network, so this second's
	// throughput *is* the link's capacity rather than a reflection of how much
	// the desktop happened to change. Back off below it, and quickly — an
	// overshoot is not merely wasteful, everything it fails to deliver is
	// sitting in a queue adding latency.
	//
	// Not backlogged: probe upward, but only if the ceiling was actually being
	// used. Otherwise this window says nothing about capacity at all.
	//
	// The asymmetry is deliberate and load-bearing. Cutting the estimate makes
	// the encoder send less, which makes the *next* window's throughput lower
	// too; if a lower reading could pull the estimate down again the two would
	// chase each other to the floor. Only a backlogged window may lower it, and
	// backing off ends the backlog, so the spiral cannot start.
	switch used := goodput / l.est; {
	case l.rx > 0 && goodput > 0 && l.rx < goodput*0.85:
		// The viewer is receiving materially less than we are sending, while we
		// are pushing hard. Everything in between is queueing, and what actually
		// arrives is what the path delivers — so that is the capacity.
		//
		// This is the signal that does not depend on backpressure reaching us.
		// A reverse proxy or an overlay in the path will absorb megabytes and
		// hide the backlog completely, and blocked writes then report a link
		// that is faster than the truth. Nothing upstream can inflate what the
		// far end counts.
		//
		// Deliberately *not* gated on the ceiling being in use, unlike probing.
		// It was, and that froze the estimate in precisely the situation it
		// exists for: a ceiling too high for the path means frames go out large
		// and slow, so the encoder never reaches 70% of it, so the one signal
		// that could have corrected the ceiling was disqualified by the symptom
		// of the ceiling being wrong. Evidence that we are over capacity is
		// evidence regardless of how hard we happen to be pushing.
		if target := l.rx * 1.05; target < l.est {
			l.est = target
		}
	case busy >= l.backlogged:
		if target := goodput * 0.9; target < l.est {
			l.est = target
		}
	case used >= probeWhenUsing:
		// Spending most of the ceiling without blocking: there may well be more
		// room, so reach for it.
		l.est *= 1.08
	default:
		// Using a fraction of the ceiling and not blocking. This window is no
		// evidence at all — a still desktop sends almost nothing whatever the
		// link can do — so hold.
		//
		// Climbing here instead was wrong in a way that made things actively
		// worse. On a link that never pushed back the estimate simply ratcheted
		// to the hard ceiling and stayed there, so the encoder budgeted ~25Mbps
		// per second of video for a connection carrying a fraction of that. The
		// bits went out as a few very large frames rather than many small ones,
		// which is why the framerate collapsed while the reported "measurement"
		// read 25Mbps: it was never a measurement, just an unchallenged guess.
	}
	l.est = clampRate(l.est)

	l.bytes, l.blocked = 0, 0
	l.windowEnd = now.Add(l.window)
}

// observeReceived records the bitrate the viewer says it is receiving.
//
// Smoothed, because a single window can legitimately show less arriving than
// was sent: data is still in flight, and a burst straddles the boundary. Only a
// sustained shortfall means the path is the limit.
func (l *linkEstimator) observeReceived(bps float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rx == 0 {
		l.rx = bps
		return
	}
	const alpha = 0.4
	l.rx = alpha*bps + (1-alpha)*l.rx
}

// rate returns the current estimate in bits per second.
func (l *linkEstimator) rate() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.est)
}

// boundSendBuffer limits how much video the kernel will absorb before a write
// blocks.
//
// The estimator reads congestion from time spent inside blocked writes, and an
// auto-tuned send buffer can run to megabytes — seconds of video on a slow
// link. That does not prevent the backlog, it only hides it while the data sits
// in a queue adding latency, and the estimate would climb the whole time. Small
// enough that blocking is prompt; large enough not to throttle the agent-to-hub
// hop, which is a LAN in the common case (256KB covers 40Mbps even at 50ms).
func boundSendBuffer(ws *websocket.Conn) {
	const sndBuf = 256 << 10
	c := ws.UnderlyingConn()
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.SetWriteBuffer(sndBuf)
	}
}

// startRateBps is where a session begins before anything has been measured.
// Deliberately modest: guessing high on a slow link floods it from the first
// frame, and the estimate climbs within seconds if there is room.
const startRateBps = 4_000_000

// rateLoop hands the measured link rate to the encoder. The encoder decides
// when it is worth acting on — changing the ceiling costs a keyframe.
func rateLoop(ctx context.Context, link *linkEstimator, encoders *encHolder, session string,
	write func(int, []byte) error, stats *frameStats) {
	const window = time.Second
	t := time.NewTicker(window)
	defer t.Stop()
	last := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := link.rate()
			encoders.get().SetRateCeiling(r)
			fs := stats.snapshot(window)
			if b, err := json.Marshal(protocol.LinkMsg{
				T: "link", Kbps: r / 1000,
				FPS: fs.FPS, Idle: fs.Idle,
				CapMs: fs.CapMs, EncMs: fs.EncMs, TxMs: fs.TxMs, TxKbps: fs.Kbps,
			}); err == nil {
				write(websocket.TextMessage, b)
			}
			// Log only real movement: this is the number to reach for when
			// someone reports the picture is blocky or the session lags, and a
			// line every two seconds would bury it.
			if last == 0 || float64(r) > float64(last)*1.3 || float64(r) < float64(last)*0.77 {
				log.Printf("session %s: link estimate %d kbps", session, r/1000)
				last = r
			}
		}
	}
}

func clampRate(v float64) float64 {
	if v < minRateBps {
		return minRateBps
	}
	if v > maxRateBps {
		return maxRateBps
	}
	return v
}
