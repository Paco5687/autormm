package server

import (
	"crypto/tls"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// idleTimeout bounds how long a relayed socket may go without sending anything.
// Both the viewer and the terminal page send an application-level ping well
// inside this, so tripping it means the peer really is gone.
const idleTimeout = 90 * time.Second

// session tracks one remote-desktop relay between a viewer and an agent.
type session struct {
	id      string
	agentID string
	kind    string
	fps     int
	quality int
	created time.Time

	agentCh chan *websocket.Conn // agent media socket delivered here
	once    sync.Once
}

type sessionRegistry struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{m: map[string]*session{}}
}

func (r *sessionRegistry) create(id, agentID, kind string, fps, quality int) *session {
	s := &session{
		id:      id,
		agentID: agentID,
		kind:    kind,
		fps:     fps,
		quality: quality,
		created: time.Now(),
		agentCh: make(chan *websocket.Conn, 1),
	}
	r.mu.Lock()
	r.m[id] = s
	r.mu.Unlock()
	return s
}

func (r *sessionRegistry) get(id string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

func (r *sessionRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, id)
}

// deliverAgent hands the agent's media socket to a waiting viewer.
func (s *session) deliverAgent(ws *websocket.Conn) bool {
	select {
	case s.agentCh <- ws:
		return true
	default:
		return false
	}
}

// relay copies messages between two sockets until either side closes. Message
// type (binary frame vs text input) is preserved and the payload is forwarded
// opaquely.
func relay(a, b *websocket.Conn) {
	const maxMsg = 16 << 20 // 16 MiB: room for a full keyframe
	a.SetReadLimit(maxMsg)
	b.SetReadLimit(maxMsg)
	boundRelayBuffers(a)
	boundRelayBuffers(b)
	done := make(chan struct{}, 2)
	go pump(a, b, done, "viewer->agent")
	go pump(b, a, done, "agent->viewer")
	<-done
	a.Close()
	b.Close()
	<-done
}

// boundRelayBuffers limits how much the kernel will absorb on a relayed socket
// before a write blocks.
//
// The agent sizes its stream to what the viewer's link was measured to carry,
// and it measures that from its own writes blocking. Backpressure only reaches
// it if every hop in between actually applies some: with an auto-tuned send
// buffer the hub cheerfully swallows megabytes of video destined for a slow
// viewer, the agent's writes never block, and it concludes the link is faster
// than it is — which is worse than not measuring at all, because it then sends
// even more. Bounding the buffer here is what makes the measurement honest.
//
// Large enough not to throttle a fast viewer (256KB covers 40Mbps at 50ms RTT),
// small enough that a slow one is felt within a measurement window.
func boundRelayBuffers(ws *websocket.Conn) {
	const sndBuf = 256 << 10
	c := ws.UnderlyingConn()
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.SetWriteBuffer(sndBuf)
	}
}

// pump copies one direction of a relayed session. It logs why it stopped:
// sessions ending unexpectedly is otherwise invisible, and "which side gave up,
// reading or writing" is the whole diagnosis.
func pump(src, dst *websocket.Conn, done chan struct{}, dir string) {
	defer func() { done <- struct{}{} }()
	for {
		src.SetReadDeadline(time.Now().Add(idleTimeout))
		mt, data, err := src.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("relay %s: read ended: %v", dir, err)
			}
			return
		}
		dst.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err := dst.WriteMessage(mt, data); err != nil {
			log.Printf("relay %s: write ended: %v", dir, err)
			return
		}
	}
}
