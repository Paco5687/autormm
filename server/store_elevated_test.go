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

// With only the elevated helper attached the host is still online (exec and
// service control work), but there is no user desktop to stream. If CanStream
// stayed true off the stale registration, Remote would still be clickable and
// open onto a black canvas — the session has nothing to bind to.
func TestCanStreamFalseWithoutInteractiveConn(t *testing.T) {
	s := NewStore(10, time.Minute, nil)
	tray, elev := &agentConn{}, &agentConn{}
	s.register(protocol.Register{AgentID: "win11", CanStream: true}, tray)
	s.register(protocol.Register{AgentID: "win11", Elevated: true, CanExec: true}, elev)

	// The user logs out (or reboots): the tray drops, the SYSTEM service stays.
	s.disconnect("win11", tray)

	v := s.viewLocked(s.hosts["win11"])
	if !v.Online {
		t.Errorf("Online = false; the elevated helper is still connected")
	}
	if v.CanStream {
		t.Errorf("CanStream = true with no interactive connection — Remote would open a black screen")
	}
	if !v.CanExec {
		t.Errorf("CanExec = false; the elevated helper can still run commands")
	}

	// Logging back in restores streaming.
	s.register(protocol.Register{AgentID: "win11", CanStream: true}, &agentConn{})
	if v := s.viewLocked(s.hosts["win11"]); !v.CanStream {
		t.Errorf("CanStream = false after the interactive agent reconnected")
	}
}

// The SYSTEM console worker follows the input desktop, so it must serve screen
// sessions in preference to the user-session agent — that is what lets Remote
// see the lock / sign-in screen. Exec still goes to the elevated helper.
func TestConsoleWorkerPreferredForScreen(t *testing.T) {
	s := NewStore(10, time.Minute, nil)
	tray := &agentConn{}
	elev := &agentConn{}
	cons := &agentConn{}
	s.register(protocol.Register{AgentID: "win11", CanStream: true}, tray)
	s.register(protocol.Register{AgentID: "win11", Elevated: true, CanExec: true}, elev)
	s.register(protocol.Register{AgentID: "win11", Console: true, CanStream: true}, cons)

	if got := s.connFor("win11"); got != cons {
		t.Errorf("screen session bound to the wrong connection; want the console worker")
	}
	if got := s.execConn("win11"); got != elev {
		t.Errorf("exec should still prefer the elevated helper, not the console worker")
	}
	if !s.canStream("win11") {
		t.Errorf("canStream = false with a console worker attached")
	}

	// The console worker can capture the lock screen, so streaming survives the
	// user logging out — unlike a plain tray-only host.
	s.disconnect("win11", tray)
	if got := s.connFor("win11"); got != cons {
		t.Errorf("console worker dropped when the user session ended")
	}
	if !s.canStream("win11") {
		t.Errorf("canStream = false after logout despite the console worker")
	}

	// Losing the console worker falls back to the user-session agent.
	s.register(protocol.Register{AgentID: "win11", CanStream: true}, tray)
	s.disconnect("win11", cons)
	if got := s.connFor("win11"); got != tray {
		t.Errorf("did not fall back to the user-session agent after the console worker dropped")
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
