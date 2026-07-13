//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// runTerminal attaches an interactive PTY shell to the media socket: raw shell
// output is sent as binary frames; the client sends TermMsg input/resize events.
func (a *Agent) runTerminal(parent context.Context, ws *websocket.Conn, ss protocol.StartSession) {
	if !a.cfg.AllowExec {
		sendErr(ws, "shell access is disabled on this host")
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/sh"
		}
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-i")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		sendErr(ws, "could not start shell: "+err.Error())
		return
	}
	defer func() {
		ptmx.Close()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()
	log.Printf("session %s: terminal started (%s)", ss.Session, shell)

	// Close the socket when the session ends so the read loop below unblocks.
	go func() {
		<-ctx.Done()
		ws.Close()
	}()

	// PTY output -> client (binary).
	go func() {
		defer cancel()
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Client -> PTY (JSON input / resize). No read deadline: idle shells are normal.
	ws.SetReadLimit(1 << 20)
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m protocol.TermMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "in":
			io.WriteString(ptmx, m.D)
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(m.Rows), Cols: uint16(m.Cols)})
			}
		}
	}
}
