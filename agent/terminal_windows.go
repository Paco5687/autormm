//go:build windows

package agent

import (
	"context"

	"github.com/gorilla/websocket"

	"github.com/Paco5687/autormm/internal/protocol"
)

// runTerminal is not yet supported on Windows agents (needs ConPTY).
func (a *Agent) runTerminal(_ context.Context, ws *websocket.Conn, _ protocol.StartSession) {
	sendErr(ws, "interactive terminal is not supported on Windows agents yet")
}
