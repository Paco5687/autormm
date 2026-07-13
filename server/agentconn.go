package server

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// agentConn wraps the persistent control WebSocket to one agent.
type agentConn struct {
	agentID   string
	ws        *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	done      chan struct{}
	execReg   *execRegistry // routes command output back to API handlers
	invReg    *invRegistry  // routes inventory responses back to API handlers
}

func newAgentConn(agentID string, ws *websocket.Conn, execReg *execRegistry, invReg *invRegistry) *agentConn {
	return &agentConn{
		agentID: agentID,
		ws:      ws,
		send:    make(chan []byte, 16),
		done:    make(chan struct{}),
		execReg: execReg,
		invReg:  invReg,
	}
}

func (c *agentConn) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	case <-c.done:
	default:
		// Backpressure: drop and close a stuck control channel.
		c.close()
	}
}

func (c *agentConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.ws.Close()
	})
}

// writePump serialises writes and sends periodic app-level pings.
func (c *agentConn) writePump() {
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.send:
			c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			b, _ := json.Marshal(protocol.Ping{Type: protocol.TypePing})
			c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
				c.close()
				return
			}
		}
	}
}

// readLoop consumes telemetry until the connection closes.
func (c *agentConn) readLoop(store *Store) {
	defer func() {
		c.close()
		store.disconnect(c.agentID, c)
	}()
	c.ws.SetReadLimit(1 << 20)
	c.ws.SetReadDeadline(time.Now().Add(45 * time.Second))
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		c.ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		var env protocol.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeMetrics:
			var m protocol.Metrics
			if err := json.Unmarshal(data, &m); err == nil {
				store.updateMetrics(c.agentID, &m)
			}
		case protocol.TypePong, protocol.TypePing:
			// keepalive; deadline already refreshed
		case protocol.TypeExecOut:
			var o protocol.ExecOutput
			if json.Unmarshal(data, &o) == nil && c.execReg != nil {
				if col := c.execReg.get(o.ExecID); col != nil {
					col.append(o.Stream, o.Data)
				}
			}
		case protocol.TypeExecDone:
			var d protocol.ExecDone
			if json.Unmarshal(data, &d) == nil && c.execReg != nil {
				if col := c.execReg.get(d.ExecID); col != nil {
					col.finish(d.ExitCode, d.Err)
				}
			}
		case protocol.TypeInventoryResp:
			var resp protocol.InventoryResponse
			if json.Unmarshal(data, &resp) == nil && c.invReg != nil {
				c.invReg.deliver(&resp)
			}
		default:
			log.Printf("agent %s: unexpected message type %q", c.agentID, env.Type)
		}
	}
}
