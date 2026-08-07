package agent

import (
	"context"
	"encoding/base64"
	"github.com/Paco5687/autormm/internal/procattr"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Paco5687/autormm/internal/protocol"
)

const (
	execDefaultTimeout = 30 * time.Second
	// Long enough for a full Windows Update install; the hub picks the actual
	// per-command timeout and this is only the backstop.
	execMaxTimeout = 60 * time.Minute
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
	procattr.Hide(cmd)
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
		return "powershell", psArgs(command)
	case "cmd":
		return "cmd", []string{"/c", command}
	}
	// default per OS
	if runtime.GOOS == "windows" {
		return "powershell", psArgs(command)
	}
	return "sh", []string{"-c", command}
}

// psArgs runs a script via -EncodedCommand rather than -Command.
//
// powershell.exe re-parses the raw command line for -Command, so a multi-line
// script — especially one containing quotes — does not necessarily arrive as
// written, and fails in ways that look like the script itself is wrong.
// -EncodedCommand takes base64 of UTF-16LE and is immune to all of it.
//
// The whole command line is still capped near 32k by the OS, and base64 of
// UTF-16 is ~2.7x the source, so scripts beyond ~12KB will not fit either way.
func psArgs(command string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(command)}
}

// encodePowerShell renders a script the way -EncodedCommand expects it.
func encodePowerShell(s string) string {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8)) // little-endian
	}
	return base64.StdEncoding.EncodeToString(buf)
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
