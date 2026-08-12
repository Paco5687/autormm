package server

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Watched services.
//
// The hub could already start, stop and restart a service, but nothing watched
// one — the alert rules were offline, CPU, memory, disk and SMART, so "tell me
// when sshd stops" was not expressible. That is the thing a monitoring tool is
// most expected to do.
//
// States are polled from the hub over the existing exec path rather than added
// to the agent's metrics, which keeps this working against agents already
// deployed. One command per host lists every watched service at once, because a
// command per service per host per minute is a lot of remote work for a handful
// of booleans.

const svcPollInterval = 60 * time.Second

// svcWatcher holds the last observed state of each watched service.
type svcWatcher struct {
	mu   sync.RWMutex
	seen map[string]map[string]bool // agent id -> service -> running
}

func newSvcWatcher() *svcWatcher {
	return &svcWatcher{seen: map[string]map[string]bool{}}
}

// states returns what was last seen for a host, or nil if it has not been
// polled. Nil matters: "not checked yet" must not read as "everything down".
func (w *svcWatcher) states(agentID string) map[string]bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	m := w.seen[agentID]
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (w *svcWatcher) set(agentID string, states map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen[agentID] = states
}

func (w *svcWatcher) forget(agentID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, agentID)
}

// serviceStatusCommand builds one command reporting the state of every named
// service, as "name=running" or "name=stopped" lines.
//
// Names are passed through a strict filter rather than quoted, because they end
// up inside a shell and a service name is not a place that needs punctuation.
func serviceStatusCommand(osName string, names []string) (shell, cmd string, ok bool) {
	var safe []string
	for _, n := range names {
		if n = strings.TrimSpace(n); safeServiceName(n) {
			safe = append(safe, n)
		}
	}
	if len(safe) == 0 {
		return "", "", false
	}
	switch osName {
	case "linux":
		// is-active answers "active" for a running unit and something else
		// otherwise, and exits non-zero when it is not running — hence the || :
		// so one stopped service does not end the loop.
		return "sh", `for s in ` + strings.Join(safe, " ") + `; do
  if systemctl is-active --quiet "$s" 2>/dev/null; then echo "$s=running"; else echo "$s=stopped"; fi
done`, true
	case "darwin":
		return "sh", `for s in ` + strings.Join(safe, " ") + `; do
  if launchctl list 2>/dev/null | grep -q "[[:space:]]$s$"; then echo "$s=running"; else echo "$s=stopped"; fi
done`, true
	case "windows":
		list := "'" + strings.Join(safe, "','") + "'"
		return "powershell", `foreach ($s in @(` + list + `)) {
  $svc = Get-Service -Name $s -ErrorAction SilentlyContinue
  if ($svc -and $svc.Status -eq 'Running') { "$s=running" } else { "$s=stopped" }
}`, true
	}
	return "", "", false
}

// safeServiceName keeps shell metacharacters out of a command built by
// concatenation. Real unit and service names only use these.
func safeServiceName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '@':
		default:
			return false
		}
	}
	return true
}

// parseServiceStates reads the command's output back into a map.
func parseServiceStates(out string) map[string]bool {
	states := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		i := strings.LastIndexByte(line, '=')
		if i <= 0 {
			continue
		}
		name, state := line[:i], strings.ToLower(strings.TrimSpace(line[i+1:]))
		if state == "running" || state == "stopped" {
			states[name] = state == "running"
		}
	}
	return states
}

// watchedServicesFor returns the services to watch on a host, from its own
// settings and any policy that covers it.
func (s *Server) watchedServicesFor(v protocol.HostView) []string {
	if s.prefs == nil {
		return nil
	}
	return s.prefs.resolveServices(v)
}

// svcWatchLoop polls watched services on every host that has any.
func (s *Server) svcWatchLoop(ctx context.Context) {
	t := time.NewTicker(svcPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollServices(ctx)
		}
	}
}

func (s *Server) pollServices(ctx context.Context) {
	var wg sync.WaitGroup
	for _, v := range s.store.views() {
		names := s.watchedServicesFor(v)
		if len(names) == 0 {
			s.svc.forget(v.AgentID)
			continue
		}
		if !v.Online {
			// An offline host already raises its own alert; reporting each of
			// its services as down too would turn one problem into five.
			s.svc.forget(v.AgentID)
			continue
		}
		shell, cmd, ok := serviceStatusCommand(v.OS, names)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			res, err := s.runOnAgent(agentID, cmd, shell, 30)
			if err != nil {
				return // leave the previous states rather than inventing new ones
			}
			s.svc.set(agentID, parseServiceStates(res.Stdout))
		}(v.AgentID)
	}
	wg.Wait()
}
