package server

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/auth"
	"github.com/Paco5687/autormm/internal/protocol"
)

// execResult is the outcome of running a command on a host.
type execResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Err       string
	Truncated bool
}

// runOnAgent sends a command to a host, waits for completion, and returns its
// captured output. Used by the /api/exec handler and the script runner.
func (s *Server) runOnAgent(agentID, command, shell string, timeoutSecs int) (*execResult, error) {
	if !s.store.canExec(agentID) {
		return nil, fmt.Errorf("host offline or command execution disabled")
	}
	conn := s.store.execConn(agentID) // prefer the elevated (SYSTEM/root) helper
	if conn == nil {
		return nil, fmt.Errorf("host offline")
	}
	if timeoutSecs <= 0 || timeoutSecs > 600 {
		timeoutSecs = 30
	}
	execID := auth.RandomID(12)
	col := s.execReg.create(execID)
	defer s.execReg.remove(execID)

	log.Printf("AUDIT exec agent=%s shell=%q cmd=%q", agentID, shell, truncateForLog(command))
	conn.sendJSON(protocol.ExecRequest{
		Type: protocol.TypeExec, ExecID: execID,
		Command: command, Shell: shell, TimeoutSecs: timeoutSecs,
	})

	select {
	case <-col.done:
		stdout, stderr, code, errStr, truncated := col.result()
		log.Printf("AUDIT exec agent=%s exit=%d", agentID, code)
		return &execResult{Stdout: stdout, Stderr: stderr, ExitCode: code, Err: errStr, Truncated: truncated}, nil
	case <-time.After(time.Duration(timeoutSecs)*time.Second + 15*time.Second):
		return nil, fmt.Errorf("timed out waiting for agent")
	}
}

func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

const execMaxOutput = 1 << 20 // 1 MiB cap on captured output per command

// execCollector accumulates a command's output until it finishes.
type execCollector struct {
	mu        sync.Mutex
	stdout    strings.Builder
	stderr    strings.Builder
	n         int
	truncated bool
	finished  bool
	exitCode  int
	errStr    string
	done      chan struct{}
}

func (c *execCollector) append(stream, data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n >= execMaxOutput {
		c.truncated = true
		return
	}
	if rem := execMaxOutput - c.n; len(data) > rem {
		data = data[:rem]
		c.truncated = true
	}
	c.n += len(data)
	if stream == "stderr" {
		c.stderr.WriteString(data)
	} else {
		c.stdout.WriteString(data)
	}
}

func (c *execCollector) finish(code int, errStr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.finished = true
		c.exitCode = code
		c.errStr = errStr
		close(c.done)
	}
}

func (c *execCollector) result() (stdout, stderr string, code int, errStr string, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String(), c.stderr.String(), c.exitCode, c.errStr, c.truncated
}

// execRegistry tracks in-flight commands by id so agent output can be routed
// back to the waiting API handler.
type execRegistry struct {
	mu sync.Mutex
	m  map[string]*execCollector
}

func newExecRegistry() *execRegistry { return &execRegistry{m: map[string]*execCollector{}} }

func (r *execRegistry) create(id string) *execCollector {
	c := &execCollector{done: make(chan struct{})}
	r.mu.Lock()
	r.m[id] = c
	r.mu.Unlock()
	return c
}

func (r *execRegistry) get(id string) *execCollector {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

func (r *execRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}
