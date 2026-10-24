package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
	"github.com/Paco5687/autormm/internal/protocol"
)

// Unenrolling: removing a host from the hub *and* the agent from the host.
//
// Removing a host (see removeHost) forgets a machine the hub has no further
// business with — a decommissioned one, where there is nothing left to
// uninstall. Unenrolling is the opposite case: the machine is alive and
// answering, and it is the enrolment itself that was a mistake. The wrong
// installer on a server, a test box that should never have been added, a
// machine changing hands.
//
// Done by hand that is four commands on the host, which is three more than it
// should be and assumes a shell on a machine you may well have enrolled
// precisely because you did not have one. So the hub asks the agent to take
// itself off, waits to watch it go, and then forgets it.

// unenrollWait bounds how long the hub waits for a host to disconnect after
// accepting the request.
//
// The agent waits delayBeforeTeardown (3s) before touching anything, then has
// to stop a service; Windows adds a service stop and a taskkill to that. Ten
// seconds is comfortably past a healthy one and still short enough that an
// operator watching the dashboard is not left wondering.
const unenrollWait = 10 * time.Second

// unenrollAck bounds how long each agent gets to accept the request.
const unenrollAck = 15 * time.Second

func (s *Server) unenrollHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		http.Error(w, "agent is required", http.StatusBadRequest)
		return
	}

	conns := s.store.controlConns(agentID)
	if len(conns) == 0 {
		// Nothing to ask. An offline host cannot be told to uninstall itself,
		// and pretending otherwise would leave the agent installed on a machine
		// the dashboard no longer lists — the one outcome this is meant to
		// prevent. Removal is the right tool for that host, and it is a
		// different button with a different warning.
		s.audit(r, "host:unenroll", agentID, "", "denied")
		http.Error(w, "this host is offline, so its agent cannot be told to remove itself. "+
			"Bring it back online to unenroll it, or use Remove to drop it from the dashboard "+
			"and uninstall the agent on the machine yourself.", http.StatusConflict)
		return
	}

	// Every half of the install is asked separately, because each can only
	// remove its own: on Windows the tray app owns an autostart entry in the
	// user's registry hive that the SYSTEM service cannot see, and the service
	// can only be deleted by something holding admin rights that the tray app
	// does not have.
	accepted, failures := s.askAgentsToUninstall(agentID, conns)

	if accepted == 0 {
		s.audit(r, "host:unenroll", agentID, strings.Join(failures, "; "), "error")
		http.Error(w, "the agent refused to uninstall itself: "+strings.Join(failures, "; "),
			http.StatusBadGateway)
		return
	}

	// Watch it actually go. The acknowledgement only says the teardown was
	// launched; a host that is still connected after all this is one whose
	// uninstall did not take, and forgetting it then would leave a running
	// agent reporting to a hub that no longer knows it — it would simply
	// register again and reappear, which looks like the button not working.
	if s.waitForDisconnect(agentID, unenrollWait) {
		removed, _ := s.store.remove(agentID)
		if removed {
			s.forgetHost(agentID)
		}
		s.audit(r, "host:unenroll", agentID, partial(failures), "ok")
		writeJSON(w, http.StatusOK, map[string]any{
			"unenrolled": agentID,
			"warnings":   failures,
		})
		return
	}

	s.audit(r, "host:unenroll", agentID, "still connected", "error")
	http.Error(w, "the agent accepted the request but is still connected after "+
		unenrollWait.String()+". It has been left on the dashboard: check the host, "+
		"and remove it by hand if its agent is really gone.", http.StatusGatewayTimeout)
}

// askAgentsToUninstall sends the request to each connection and collects the
// acknowledgements, returning how many accepted and what the others said.
func (s *Server) askAgentsToUninstall(agentID string, conns []*agentConn) (accepted int, failures []string) {
	type pending struct {
		execID string
		col    *execCollector
	}
	var waits []pending

	for _, conn := range conns {
		execID := auth.RandomID(12)
		col := s.execReg.create(execID)
		defer s.execReg.remove(execID)
		waits = append(waits, pending{execID: execID, col: col})
		conn.sendJSON(protocol.UninstallRequest{Type: protocol.TypeUninstall, ExecID: execID})
	}

	log.Printf("AUDIT unenroll agent=%s asked %d connection(s)", agentID, len(conns))

	// One deadline across all of them rather than one each: they were asked at
	// the same moment and are doing the same work, so the slowest is the only
	// one that matters.
	deadline := time.After(unenrollAck)
	for _, p := range waits {
		select {
		case <-p.col.done:
			_, _, code, errStr, _ := p.col.result()
			if code == 0 && errStr == "" {
				accepted++
				continue
			}
			if errStr == "" {
				errStr = fmt.Sprintf("exit %d", code)
			}
			failures = append(failures, errStr)
		case <-deadline:
			failures = append(failures, "timed out waiting for the agent to answer")
		}
	}
	log.Printf("AUDIT unenroll agent=%s accepted=%d failures=%v", agentID, accepted, failures)
	return accepted, failures
}

// waitForDisconnect blocks until the host drops every connection, or the
// timeout passes.
func (s *Server) waitForDisconnect(agentID string, within time.Duration) bool {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	deadline := time.After(within)
	for {
		if !s.store.stillConnected(agentID) {
			return true
		}
		select {
		case <-t.C:
		case <-deadline:
			return !s.store.stillConnected(agentID)
		}
	}
}

// partial describes a half-success for the audit log: the host is gone, but
// something on it said no on the way out.
func partial(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	return "partial: " + strings.Join(failures, "; ")
}
