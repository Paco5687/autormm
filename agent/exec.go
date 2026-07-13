package agent

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

const (
	execDefaultTimeout = 30 * time.Second
	execMaxTimeout     = 10 * time.Minute
)

// runExec runs a command from the server and streams its output back through
// out, finishing with an ExecDone message.
func (a *Agent) runExec(parent context.Context, out chan<- any, req protocol.ExecRequest) {
	done := func(code int, errStr string) {
		select {
		case out <- protocol.ExecDone{Type: protocol.TypeExecDone, ExecID: req.ExecID, ExitCode: code, Err: errStr}:
		case <-parent.Done():
		}
	}

	if !a.cfg.AllowExec {
		done(-1, "remote command execution is disabled on this host")
		return
	}

	name, args := shellFor(req.Shell, req.Command)
	if name == "" {
		done(-1, "no shell available")
		return
	}

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = execDefaultTimeout
	}
	if timeout > execMaxTimeout {
		timeout = execMaxTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &chunkWriter{out: out, done: parent.Done(), execID: req.ExecID, stream: "stdout"}
	cmd.Stderr = &chunkWriter{out: out, done: parent.Done(), execID: req.ExecID, stream: "stderr"}

	if err := cmd.Start(); err != nil {
		done(-1, err.Error())
		return
	}
	err := cmd.Wait()

	code := 0
	errStr := ""
	if ctx.Err() == context.DeadlineExceeded {
		code, errStr = -1, "timed out after "+timeout.String()
	} else if err != nil {
		code = cmd.ProcessState.ExitCode()
		if code < 0 {
			errStr = err.Error()
		}
	}
	done(code, errStr)
}

// shellFor returns the executable + args for the requested shell.
func shellFor(shell, command string) (string, []string) {
	switch shell {
	case "sh":
		return "sh", []string{"-c", command}
	case "bash":
		return "bash", []string{"-c", command}
	case "powershell", "pwsh":
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", command}
	case "cmd":
		return "cmd", []string{"/c", command}
	}
	// default per OS
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", command}
	}
	return "sh", []string{"-c", command}
}

// chunkWriter forwards each write as an ExecOutput message.
type chunkWriter struct {
	out    chan<- any
	done   <-chan struct{}
	execID string
	stream string
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	msg := protocol.ExecOutput{
		Type:   protocol.TypeExecOut,
		ExecID: w.execID,
		Stream: w.stream,
		Data:   strings.ToValidUTF8(string(p), ""),
	}
	select {
	case w.out <- msg:
	case <-w.done:
	}
	return len(p), nil
}
