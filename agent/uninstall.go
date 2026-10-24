package agent

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Taking the agent off a machine, from the hub.
//
// Enrolling a host is one pasted command; un-enrolling it used to be four
// manual ones on the machine itself, which is the wrong way round — the case
// you most want a button for is the host that should never have been enrolled,
// and that is exactly the host you are least likely to still have a shell on.
//
// The awkward part is that the thing being removed is the thing doing the
// removing. An agent cannot stop its own service and then carry on deleting
// files, because stopping the service is what kills it; on Windows it cannot
// delete its own running executable at all. So none of the work happens in
// this process. The agent writes a small script, hands it to the system in a
// way that outlives the agent, acknowledges the request while it still has a
// socket to answer on, and waits to be stopped.

// uninstaller is a prepared teardown: a script written to disk and the command
// that launches it detached from this process.
type uninstaller struct {
	script string    // path to the script, which deletes itself last
	launch *exec.Cmd // starts the script outside this process's lifetime
	// what describes the plan in the log, so an uninstall that goes wrong
	// leaves behind what it was going to do.
	what string
}

// delayBeforeTeardown is how long the script waits before touching anything.
//
// It covers the gap between this process acknowledging the request and the ack
// actually reaching the hub: the writer goroutine has to pick the message off
// the channel and put it on the wire. A teardown that began immediately would
// sometimes race the reply and leave the hub reporting a failure for an
// uninstall that in fact succeeded — the worst of both, since the host is gone
// and the dashboard still lists it.
const delayBeforeTeardown = 3 * time.Second

// runUninstall acknowledges the request, then launches the teardown.
func (a *Agent) runUninstall(out chan<- any, req protocol.UninstallRequest) {
	done := protocol.ExecDone{Type: protocol.TypeExecDone, ExecID: req.ExecID}

	u, err := a.planUninstall()
	if err != nil {
		log.Printf("uninstall: refusing: %v", err)
		done.ExitCode = 1
		done.Err = err.Error()
		send(out, done)
		return
	}

	// Launch before acknowledging. If the launch fails there is still a working
	// agent here and a hub that can be told so, which is a far better place to
	// end up than a successful-looking reply and a machine nobody tidied.
	if err := u.launch.Start(); err != nil {
		log.Printf("uninstall: could not launch teardown (%s): %v", u.what, err)
		done.ExitCode = 1
		done.Err = "could not launch the uninstall: " + err.Error()
		send(out, done)
		return
	}
	// Released rather than waited for: the point of the script is to outlive
	// this process, so there is nobody here to reap it.
	if u.launch.Process != nil {
		_ = u.launch.Process.Release()
	}

	log.Printf("uninstall: teardown launched (%s); this agent stops in %s", u.what, delayBeforeTeardown)
	send(out, done)
}

// writeScript puts the teardown script somewhere it can be run from and
// returns its path. It is written executable and owner-only: it runs as root
// on a system install, and a world-writable root script is a privilege
// escalation sitting in the temp directory.
func writeScript(content, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("cannot write the uninstall script: %w", err)
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("cannot write the uninstall script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("cannot write the uninstall script: %w", err)
	}
	if err := os.Chmod(name, 0o700); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("cannot make the uninstall script executable: %w", err)
	}
	return name, nil
}

// send puts a message on the agent's outbound channel without blocking the
// caller forever if the writer has gone away with the connection.
func send(out chan<- any, msg any) {
	select {
	case out <- msg:
	case <-time.After(5 * time.Second):
		log.Printf("uninstall: no writer took the acknowledgement")
	}
}
