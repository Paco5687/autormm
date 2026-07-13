package agent

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/shirou/gopsutil/v3/process"

	"github.com/Paco5687/autormm/internal/protocol"
)

// restartProc stops a process and relaunches it with the same command line and
// working directory. Best-effort: it can only relaunch the exact argv, so it
// won't restore parent/session context, and service-managed processes are better
// restarted via the service controls.
func (a *Agent) restartProc(parent context.Context, out chan<- any, req protocol.ProcRestartRequest) {
	done := func(code int, errStr string) {
		select {
		case out <- protocol.ExecDone{Type: protocol.TypeExecDone, ExecID: req.ExecID, ExitCode: code, Err: errStr}:
		case <-parent.Done():
		}
	}
	if !a.cfg.AllowExec {
		done(-1, "actions are disabled on this host")
		return
	}
	pid := int32(req.PID)
	p, err := process.NewProcess(pid)
	if err != nil {
		done(-1, fmt.Sprintf("no process %d", req.PID))
		return
	}
	argv, err := p.CmdlineSlice()
	if err != nil || len(argv) == 0 || argv[0] == "" {
		done(-1, "cannot read the process command line to relaunch it")
		return
	}
	cwd, _ := p.Cwd()

	if err := p.Terminate(); err != nil {
		if kerr := p.Kill(); kerr != nil {
			done(-1, "failed to stop process: "+err.Error())
			return
		}
	}
	waitGone(pid, 5*time.Second) // let it exit and release ports/locks

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		done(-1, "process stopped but relaunch failed: "+err.Error())
		return
	}
	done(0, "") // running independently; don't Wait
}

func waitGone(pid int32, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if exists, err := process.PidExists(pid); err == nil && !exists {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
