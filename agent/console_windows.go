//go:build windows

package agent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const invalidSession = 0xFFFFFFFF

// SuperviseConsoleWorker keeps a copy of this binary running inside the active
// console session, where it can capture and drive the lock / sign-in screen.
//
// The elevated helper itself lives in session 0 and can never see a user
// desktop, so it acts purely as a launcher: it obtains a SYSTEM token for the
// console session and starts the worker there with -console-worker. If the
// console session changes (fast user switching, RDP console reconnect) or the
// worker dies, it relaunches.
//
// Every failure here is non-fatal — the host simply behaves as before, with the
// user-session agent serving the screen whenever someone is signed in.
func (a *Agent) superviseConsole(ctx context.Context) {
	var (
		mu      sync.Mutex
		proc    *os.Process
		session uint32 = invalidSession
	)
	stop := func() {
		mu.Lock()
		defer mu.Unlock()
		if proc != nil {
			_ = proc.Kill()
			proc = nil
		}
		session = invalidSession
	}
	defer stop()

	// Earlier builds orphaned workers on self-update (os.Exit skips cleanup), so a
	// host can already have several running, all capturing the screen. Reap them
	// once at startup; the job object prevents new ones from accumulating.
	if n := killStrayWorkers(); n > 0 {
		log.Printf("console worker: reaped %d orphaned worker(s) from a previous run", n)
	}

	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		sid := windows.WTSGetActiveConsoleSessionId()
		switch {
		case sid == invalidSession || sid == 0:
			// No attached console session (or only session 0, which has no user
			// desktop). Nothing worth running.
			stop()
		case proc != nil && sid == session && processAlive(proc):
			// Healthy worker on the right session — leave it be.
		default:
			stop()
			p, err := a.launchConsoleWorker(sid)
			if err != nil {
				log.Printf("console worker: launch in session %d failed: %v", sid, err)
			} else {
				mu.Lock()
				proc, session = p, sid
				mu.Unlock()
				log.Printf("console worker: started pid %d in session %d", p.Pid, sid)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func processAlive(p *os.Process) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.Pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// launchConsoleWorker starts this executable in the given session with
// -console-worker, on the interactive window station's default desktop. The
// worker re-attaches itself to whichever desktop holds input, including the
// secure one, once it is running.
func (a *Agent) launchConsoleWorker(session uint32) (*os.Process, error) {
	// Tie the worker's lifetime to this process. Self-update exits via os.Exit,
	// which skips every defer — without this the worker survived, the restarted
	// helper spawned another, and the orphans accumulated: several processes all
	// capturing the screen (slow, jittery) and displacing each other's hub
	// connection. A job object with KILL_ON_JOB_CLOSE is enforced by the kernel
	// when our handle closes, however abruptly we die.
	if err := ensureWorkerJob(); err != nil {
		log.Printf("console worker: job object unavailable (%v) -- continuing without it", err)
	}

	token, err := consoleSessionToken(session)
	if err != nil {
		return nil, err
	}
	defer token.Close()

	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{
		exe,
		"-server", a.cfg.Server,
		"-token", a.cfg.EnrollToken,
		"-id", a.cfg.AgentID,
		"-console-worker",
	}
	if a.cfg.Insecure {
		args = append(args, "-insecure")
	}

	appName, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return nil, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return nil, err
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\Default`)
	if err != nil {
		return nil, err
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Desktop = desktop
	// Hide the worker's window. It is a console binary launched into an
	// interactive session, so without this it shows a visible console window on
	// the user's desktop.
	si.Flags = windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_HIDE
	var pi windows.ProcessInformation
	// nil environment => inherit the launcher's (SYSTEM) block, which is enough
	// for the worker to run; it needs only its command-line flags. CREATE_NO_WINDOW
	// runs the console binary with no console window at all (do NOT combine with
	// CREATE_NEW_CONSOLE, which forces a visible one).
	// CREATE_BREAKAWAY_FROM_JOB is deliberately NOT set: the child must stay in
	// our job so kill-on-close applies. CREATE_SUSPENDED lets us assign it to the
	// job before it runs, closing the window where it could escape.
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcessAsUser(token, appName, cmdLine, nil, nil, false,
		flags, nil, nil, &si, &pi); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if workerJob != 0 {
		if err := windows.AssignProcessToJobObject(workerJob, pi.Process); err != nil {
			log.Printf("console worker: AssignProcessToJobObject: %v", err)
		}
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return nil, err
	}
	return os.FindProcess(int(pi.ProcessId))
}

// killStrayWorkers terminates leftover console workers from a previous run of
// this helper and returns how many it stopped.
//
// It only targets our own executable running outside session 0: the helper (this
// process) is the session-0 instance, and workers are the copies it launched into
// the console session. The tray agent is a different binary, so it is untouched.
func killStrayWorkers() int {
	self, err := os.Executable()
	if err != nil {
		return 0
	}
	want := strings.ToLower(filepath.Base(self))
	me := uint32(os.Getpid())

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	killed := 0
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == me || strings.ToLower(windows.UTF16ToString(e.ExeFile[:])) != want {
			continue
		}
		var sid uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &sid) != nil || sid == 0 {
			continue // session 0 is a helper, not a worker
		}
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, e.ProcessID)
		if err != nil {
			continue
		}
		if windows.TerminateProcess(h, 0) == nil {
			killed++
		}
		windows.CloseHandle(h)
	}
	return killed
}

// workerJob holds the console workers so they die with this process.
var workerJob windows.Handle

func ensureWorkerJob() error {
	if workerJob != 0 {
		return nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return err
	}
	workerJob = h
	return nil
}

// consoleSessionToken returns a primary SYSTEM token bound to the console
// session, so the worker runs as SYSTEM there.
//
// Running as SYSTEM (not the signed-in user) is deliberate: SYSTEM can capture
// and inject on any desktop — the user's, the lock screen, and UAC — and can
// write its log, whereas a user-identity worker inheriting the service's
// environment could do none of those reliably. The helper is already SYSTEM, so
// we duplicate our own token and retarget it at the console session; winlogon's
// token is a fallback if that ever fails.
func consoleSessionToken(session uint32) (windows.Token, error) {
	var self windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY, &self)
	if err == nil {
		defer self.Close()
		if tok, derr := duplicatePrimary(self, session); derr == nil {
			return tok, nil
		}
	}
	src, err := winlogonToken(session)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	return duplicatePrimary(src, session)
}

func duplicatePrimary(src windows.Token, session uint32) (windows.Token, error) {
	var dup windows.Token
	if err := windows.DuplicateTokenEx(src, windows.MAXIMUM_ALLOWED, nil,
		windows.SecurityImpersonation, windows.TokenPrimary, &dup); err != nil {
		return 0, err
	}
	// Pin the duplicate to the target session so the new process lands there.
	if err := windows.SetTokenInformation(dup, windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session))); err != nil {
		dup.Close()
		return 0, err
	}
	return dup, nil
}

// winlogonToken opens the token of winlogon.exe in the given session. It always
// runs as SYSTEM on the secure desktop — exactly the context needed to capture
// the sign-in screen, before any user has logged in.
func winlogonToken(session uint32) (windows.Token, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "winlogon.exe") {
			continue
		}
		var sid uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &sid) != nil || sid != session {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, e.ProcessID)
		if err != nil {
			continue
		}
		var tok windows.Token
		err = windows.OpenProcessToken(h, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok)
		windows.CloseHandle(h)
		if err == nil {
			return tok, nil
		}
	}
	return 0, os.ErrNotExist
}
