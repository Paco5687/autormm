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
	// Not backlogged: the measurement says nothing about capacity, because we
	// may simply have had nothing to send. Probe upward gently.
	//
	// The asymmetry is deliberate and load-bearing. Cutting the estimate makes
	// the encoder send less, which makes the *next* window's throughput lower
	// too; if a lower reading could pull the estimate down again the two would
	// chase each other to the floor. Only a backlogged window may lower it, and
	// backing off ends the backlog, so the spiral cannot start.
	if busy >= l.backlogged {
		if target := goodput * 0.9; target < l.est {
			l.est = target
		}
	} else {
		l.est *= 1.08
	}
	l.est = clampRate(l.est)

	l.bytes, l.blocked = 0, 0
	l.windowEnd = now.Add(l.window)
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
	write func(int, []byte) error) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	last := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := link.rate()
			encoders.get().SetRateCeiling(r)
			if b, err := json.Marshal(protocol.LinkMsg{T: "link", Kbps: r / 1000}); err == nil {
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
