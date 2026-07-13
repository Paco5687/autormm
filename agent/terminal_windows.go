//go:build windows

package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/UserExistsError/conpty"
	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// runTerminal attaches an interactive PowerShell (via the Windows ConPTY API) to
// the media socket: raw console output is sent as binary frames; the client
// sends TermMsg input/resize events. Mirrors the Unix PTY implementation.
func (a *Agent) runTerminal(parent context.Context, ws *websocket.Conn, _ protocol.StartSession) {
	if !a.cfg.AllowExec {
		sendErr(ws, "shell access is disabled on this host")
		return
	}
	if !conpty.IsConPtyAvailable() {
		sendErr(ws, "terminal needs Windows 10 1809+ (ConPTY) on this host")
		return
	}
	cpty, err := conpty.Start("powershell.exe -NoLogo", conpty.ConPtyDimensions(120, 30))
	if err != nil {
		sendErr(ws, "failed to start PowerShell: "+err.Error())
		return
	}
	// ConPty.Close() closes raw Windows handles and is NOT idempotent, so guard
	// it -- a double close can crash the process.
	var closeOnce sync.Once
	closeCpty := func() { closeOnce.Do(func() { cpty.Close() }) }
	defer closeCpty()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var wmu sync.Mutex
	writeBin := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(websocket.BinaryMessage, b)
	}

	// console output -> client
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := cpty.Read(buf)
			if n > 0 {
				if writeBin(append([]byte(nil), buf[:n]...)) != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				cancel()
				return
			}
		}
	}()

	// unblock the ConPTY read and the ws read on shutdown
	go func() {
		<-ctx.Done()
		closeCpty()
		ws.Close()
	}()

	// client input/resize -> console
	for {
		mt, data, rerr := ws.ReadMessage()
		if rerr != nil {
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
			cpty.Write([]byte(m.D))
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				cpty.Resize(m.Cols, m.Rows)
			}
		}
	}
}
