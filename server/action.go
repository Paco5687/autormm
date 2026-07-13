package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
	"github.com/Paco5687/autormm/internal/protocol"
)

// serviceNameRe restricts service names to safe characters so they can't inject
// shell metacharacters (the caller is already exec-trusted, but keep it clean).
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,128}$`)

type actionRequest struct {
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind"`   // "proc" | "service"
	Action  string `json:"action"` // proc: kill|force ; service: start|stop|restart
	PID     int    `json:"pid,omitempty"`
	Service string `json:"service,omitempty"`
}

// handleAction kills a process or controls a service on a host by running the
// platform-appropriate command through the existing exec path.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req actionRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	osName := s.store.osFor(req.AgentID)
	if osName == "" {
		http.Error(w, "host offline", http.StatusConflict)
		return
	}

	// Process restart is handled agent-side (capture cmdline, stop, relaunch).
	if req.Kind == "proc" && req.Action == "restart" {
		if req.PID <= 0 {
			http.Error(w, "invalid pid", http.StatusBadRequest)
			return
		}
		res, err := s.restartProcOnAgent(req.AgentID, req.PID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": res.ExitCode == 0 && res.Err == "", "exit_code": res.ExitCode, "err": res.Err,
		})
		return
	}

	cmd, shell, err := buildActionCommand(osName, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.runOnAgent(req.AgentID, cmd, shell, 30)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        res.ExitCode == 0 && res.Err == "",
		"exit_code": res.ExitCode,
		"output":    strings.TrimSpace(res.Stdout + res.Stderr),
		"err":       res.Err,
	})
}

// restartProcOnAgent asks the agent to restart a process and waits for the
// result, correlated through the exec registry (agent replies with ExecDone).
func (s *Server) restartProcOnAgent(agentID string, pid int) (*execResult, error) {
	if !s.store.canExec(agentID) {
		return nil, fmt.Errorf("host offline or command execution disabled")
	}
	conn := s.store.connFor(agentID)
	if conn == nil {
		return nil, fmt.Errorf("host offline")
	}
	execID := auth.RandomID(12)
	col := s.execReg.create(execID)
	defer s.execReg.remove(execID)

	log.Printf("AUDIT proc-restart agent=%s pid=%d", agentID, pid)
	conn.sendJSON(protocol.ProcRestartRequest{Type: protocol.TypeProcRestart, ExecID: execID, PID: pid})

	select {
	case <-col.done:
		_, _, code, errStr, _ := col.result()
		log.Printf("AUDIT proc-restart agent=%s pid=%d exit=%d", agentID, pid, code)
		return &execResult{ExitCode: code, Err: errStr}, nil
	case <-time.After(25 * time.Second):
		return nil, fmt.Errorf("timed out waiting for agent")
	}
}

// buildActionCommand maps an action to a shell command for the host's OS.
func buildActionCommand(osName string, req actionRequest) (cmd, shell string, err error) {
	win := osName == "windows"
	switch req.Kind {
	case "proc":
		if req.PID <= 0 {
			return "", "", fmt.Errorf("invalid pid")
		}
		pid := strconv.Itoa(req.PID)
		if win {
			flag := ""
			if req.Action == "force" {
				flag = "/F "
			}
			return "taskkill " + flag + "/PID " + pid, "", nil
		}
		sig := "TERM"
		if req.Action == "force" {
			sig = "KILL"
		}
		return "kill -" + sig + " " + pid, "", nil

	case "service":
		if !serviceNameRe.MatchString(req.Service) {
			return "", "", fmt.Errorf("invalid service name")
		}
		if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
			return "", "", fmt.Errorf("invalid service action")
		}
		if win {
			verb := map[string]string{"start": "Start-Service", "stop": "Stop-Service", "restart": "Restart-Service"}[req.Action]
			return verb + " -Name '" + req.Service + "'", "powershell", nil
		}
		return "systemctl " + req.Action + " " + req.Service, "", nil
	}
	return "", "", fmt.Errorf("unknown action kind")
}
