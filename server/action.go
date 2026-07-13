package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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
