// Package agent implements the autormm host agent: it maintains an outbound
// control connection to the server, pushes telemetry, and serves remote-desktop
// sessions on demand.
package agent

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/capture"
	"github.com/Paco5687/autormm/internal/metrics"
	"github.com/Paco5687/autormm/internal/protocol"
)

const Version = "0.1.0"

// Config configures the agent.
type Config struct {
	Server      string        // e.g. https://rmm.example.com or ws://host:8765
	EnrollToken string        // must match the server's enroll token
	AgentID     string        // stable id; defaults to hostname
	Tags        string        // free-form labels
	Interval    time.Duration // metrics push interval
	Insecure    bool          // skip TLS verify (self-signed homelab certs)
	AllowExec   bool          // permit remote command execution
}

// Agent is a running host agent.
type Agent struct {
	cfg       Config
	collector *metrics.Collector
	hostname  string
	os        string
	platform  string
	arch      string
	dialer    *websocket.Dialer
}

// New builds an agent from cfg, filling defaults.
func New(cfg Config) *Agent {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	host, osName, platform, arch := metrics.HostInfo()
	if cfg.AgentID == "" {
		cfg.AgentID = host
	}
	d := *websocket.DefaultDialer
	d.HandshakeTimeout = 15 * time.Second
	if cfg.Insecure {
		d.TLSClientConfig = insecureTLS()
	}
	return &Agent{
		cfg:       cfg,
		collector: metrics.New(8),
		hostname:  host,
		os:        osName,
		platform:  platform,
		arch:      arch,
		dialer:    &d,
	}
}

// Run connects and reconnects until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := a.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("connection lost: %v — reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// session runs one connected lifetime of the control channel.
func (a *Agent) session(ctx context.Context) error {
	ctrlURL, err := a.controlURL()
	if err != nil {
		return err
	}
	header := http.Header{"Authorization": {"Bearer " + a.cfg.EnrollToken}}
	ws, _, err := a.dialer.DialContext(ctx, ctrlURL, header)
	if err != nil {
		return err
	}
	defer ws.Close()

	reg := protocol.Register{
		Type:         protocol.TypeRegister,
		AgentID:      a.cfg.AgentID,
		Hostname:     a.hostname,
		OS:           a.os,
		Platform:     a.platform,
		Arch:         a.arch,
		AgentVersion: Version,
		CanStream:    capture.Available(),
		CanExec:      a.cfg.AllowExec,
		EncoderCaps:  capture.EncoderCaps(), // jpeg-tile always; webcodecs-h264 if ffmpeg present
		Tags:         a.cfg.Tags,
	}
	if err := ws.WriteJSON(reg); err != nil {
		return err
	}
	log.Printf("registered with %s as %q (stream=%v)", a.cfg.Server, a.cfg.AgentID, reg.CanStream)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// ReadMessage below does not observe ctx; close the socket on shutdown so
	// the read unblocks promptly (otherwise the agent lingers until the read
	// deadline and the server keeps seeing it online).
	go func() {
		<-ctx.Done()
		ws.Close()
	}()

	// writes are serialised through this channel
	out := make(chan any, 8)
	go a.writer(ctx, ws, out)
	go a.metricsLoop(ctx, out)

	// read loop
	ws.SetReadLimit(1 << 20)
	for {
		ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case protocol.TypePing:
			select {
			case out <- protocol.Pong{Type: protocol.TypePong}:
			default:
			}
		case protocol.TypeStartSession:
			var ss protocol.StartSession
			if json.Unmarshal(data, &ss) == nil {
				go a.startSession(ctx, ss)
			}
		case protocol.TypeStopSession:
			// media sockets close on their own when the relay ends; nothing to do
		case protocol.TypeExec:
			var req protocol.ExecRequest
			if json.Unmarshal(data, &req) == nil {
				go a.runExec(ctx, out, req)
			}
		case protocol.TypeInventory:
			var req protocol.InventoryRequest
			if json.Unmarshal(data, &req) == nil {
				go a.runInventory(ctx, out, req)
			}
		}
	}
}

func (a *Agent) writer(ctx context.Context, ws *websocket.Conn, out <-chan any) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-out:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

func (a *Agent) metricsLoop(ctx context.Context, out chan<- any) {
	// Prime CPU/net counters immediately, then send on the interval.
	a.collector.Collect()
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m := a.collector.Collect()
			m.Type = protocol.TypeMetrics
			select {
			case out <- m:
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}

func (a *Agent) controlURL() (string, error) {
	return a.wsURL("/agent/ws", nil)
}

// wsURL converts the configured server base into a ws/wss URL for path.
func (a *Agent) wsURL(path string, q url.Values) (string, error) {
	base := strings.TrimRight(a.cfg.Server, "/")
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	case "":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
