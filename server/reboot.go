package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// Rebooting a host on request, as opposed to rebooting to finish applying
// updates. The patch flow has its own copy of these commands with a message
// about updates, which is the right thing to tell someone sitting at the
// machine in that context and the wrong thing here.
const (
	// One script for Linux and macOS. Backgrounded with a moment's delay so the
	// exec reply reaches the hub before the host goes away — otherwise every
	// successful reboot looks like a failed command.
	unixRebootNow = `
if [ "$(id -u)" != "0" ]; then
  # A desktop session is often permitted to reboot via polkit even unprivileged,
  # so try that before giving up.
  if command -v systemctl >/dev/null 2>&1 && systemctl reboot 2>/dev/null; then echo "reboot scheduled"; exit 0; fi
  echo "reboot needs root on this host: install the elevated helper, or run 'sudo shutdown -r now' there"; exit 1
fi
( sleep 2; { command -v systemctl >/dev/null 2>&1 && systemctl reboot; } || shutdown -r now || reboot ) >/dev/null 2>&1 &
echo "reboot scheduled"`

	// A short warning rather than none: whoever is sitting at the machine gets a
	// countdown and a reason, which costs the operator fifteen seconds and can
	// save someone's unsaved work.
	windowsRebootNow = `shutdown.exe /r /t 15 /c "autormm: an administrator requested a reboot" | Out-Null; Write-Output 'reboot scheduled'`
)

// rebootCommand returns the shell and command that reboot a host of this OS.
func rebootCommand(osName string) (shell, cmd string, ok bool) {
	switch osName {
	case "linux", "darwin":
		return "sh", unixRebootNow, true
	case "windows":
		return "powershell", windowsRebootNow, true
	}
	return "", "", false
}

// handleReboot reboots a host on request.
//
// Deliberately separate from the patch flow's reboot, which is only offered on
// platforms autormm knows how to patch and tells the user it is finishing
// updates. Restarting a machine is a reasonable thing to want on its own.
//
// Routing goes through runOnAgent, which prefers the elevated (SYSTEM/root)
// helper when one is attached and falls back to the user-session agent. That
// matters here: an unprivileged agent may not be allowed to reboot at all, and
// the script says so rather than failing silently.
func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	shell, cmd, ok := rebootCommand(s.store.osFor(req.AgentID))
	if !ok {
		http.Error(w, "reboot is not supported on this host", http.StatusBadRequest)
		return
	}

	// Logged in its own right, not just as the exec it turns into: "who
	// restarted that machine" is a question worth being able to answer directly.
	log.Printf("AUDIT reboot agent=%s from=%s", req.AgentID, r.RemoteAddr)

	res, err := s.runOnAgent(req.AgentID, cmd, shell, 30)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": err.Error()})
		return
	}
	// The script exits non-zero and explains itself when it lacks the privilege
	// to reboot, so surface that rather than reporting a success the host did
	// not perform.
	out := strings.TrimSpace(res.Stdout + res.Stderr)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     res.ExitCode == 0 && res.Err == "",
		"output": out,
	})
}
