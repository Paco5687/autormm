package server

import (
	"testing"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// The Windows elevated helper (#49) is a second connection for the SAME host.
// Screen/input sessions must always bind to the interactive (user-session)
// connection — a SYSTEM service in session 0 cannot see the user's desktop or
// inject input into it — while exec/service work prefers the elevated one.
func TestElevatedHelperDoesNotStealInteractiveConn(t *testing.T) {
	interactive := protocol.Register{AgentID: "win11", Elevated: false, CanStream: true}
	elevated := protocol.Register{AgentID: "win11", Elevated: true, CanStream: false}

	// The service starts at boot, so it often registers BEFORE the user logs in
	// and the tray connects. Both orders must end up routing the same way.
	for _, tc := range []struct {
		name  string
		first protocol.Register
		last  protocol.Register
	}{
		{"elevated first (boot before logon)", elevated, interactive},
		{"interactive first", interactive, elevated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(10, time.Minute, nil)
			cFirst, cLast := &agentConn{}, &agentConn{}
			s.register(tc.first, cFirst)
			s.register(tc.last, cLast)

			wantInteractive, wantElevated := cFirst, cLast
			if tc.first.Elevated {
				wantInteractive, wantElevated = cLast, cFirst
			}

			if got := s.connFor("win11"); got != wantInteractive {
				t.Errorf("connFor returned the wrong connection: sessions would bind to the SYSTEM helper")
			}
			if got := s.execConn("win11"); got != wantElevated {
				t.Errorf("execConn should prefer the elevated helper")
			}
			if !s.hasElevated("win11") {
				t.Errorf("hasElevated = false, want true")
			}
			// The host's advertised identity must come from the interactive
			// agent, or the dashboard shows the host as non-streamable.
			if v := s.viewLocked(s.hosts["win11"]); !v.CanStream {
				t.Errorf("CanStream = false; the elevated helper overwrote the interactive registration")
			}
		})
	}
}

// Losing the elevated helper must not take the interactive connection down.
func TestElevatedDisconnectKeepsInteractive(t *testing.T) {
	s := NewStore(10, time.Minute, nil)
	tray, elev := &agentConn{}, &agentConn{}
	s.register(protocol.Register{AgentID: "win11", CanStream: true}, tray)
	s.register(protocol.Register{AgentID: "win11", Elevated: true}, elev)

	s.disconnect("win11", elev)
	if got := s.connFor("win11"); got != tray {
		t.Fatalf("interactive connection lost when the elevated helper dropped")
	}
	if s.hasElevated("win11") {
		t.Errorf("hasElevated = true after the helper disconnected")
	}
}
