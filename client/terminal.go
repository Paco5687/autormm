package client

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Shell opens an interactive PTY shell on a host and bridges it to the local
// terminal until the remote shell exits.
func (c *Client) Shell(agentID string) error {
	sess, err := c.CreateSession(agentID, protocol.SessionTerminal, 0, 0)
	if err != nil {
		return err
	}

	dialer := *websocket.DefaultDialer
	if c.cfg.Insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	wsURL := toWS(c.cfg.Server) + sess.WSPath + "?token=" + url.QueryEscape(sess.Token)
	ws, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer ws.Close()

	var writeMu sync.Mutex
	send := func(m protocol.TermMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteJSON(m)
	}

	// Put the local terminal in raw mode when attached to one.
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, old)
		}
		sendResize := func() {
			if w, h, err := term.GetSize(fd); err == nil {
				send(protocol.TermMsg{T: "resize", Cols: w, Rows: h})
			}
		}
		sendResize()
		stop := watchResize(fd, sendResize)
		defer close(stop)
	}

	errc := make(chan error, 2)

	// Remote output -> stdout.
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				os.Stdout.Write(data)
			case websocket.TextMessage:
				var m map[string]string
				if json.Unmarshal(data, &m) == nil && m["t"] == "error" {
					errc <- fmt.Errorf("%s", m["message"])
					return
				}
			}
		}
	}()

	// stdin -> remote. Reaching EOF here does NOT end the session: we send EOT
	// so the remote shell exits, then let the output side drive teardown when
	// the shell actually closes. Only a send failure aborts.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if serr := send(protocol.TermMsg{T: "in", D: string(buf[:n])}); serr != nil {
					errc <- serr
					return
				}
			}
			if err != nil {
				send(protocol.TermMsg{T: "in", D: "\x04"}) // EOT so the shell exits
				return
			}
		}
	}()

	err = <-errc
	if err == nil || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
		return nil
	}
	if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
		return nil
	}
	return err
}

func toWS(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	if strings.HasPrefix(base, "http://") {
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}
