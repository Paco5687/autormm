//go:build windows

package agent

import (
	"context"
	"log"
	"os"
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
	var pi windows.ProcessInformation
	// nil environment => inherit the launcher's (SYSTEM) block, which is enough
	// for the worker to run; it needs only its command-line flags.
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_CONSOLE)
	if err := windows.CreateProcessAsUser(token, appName, cmdLine, nil, nil, false,
		flags, nil, nil, &si, &pi); err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return os.FindProcess(int(pi.ProcessId))
}

// consoleSessionToken returns a primary SYSTEM token bound to the console
// session.
//
// WTSQueryUserToken covers the common case, including a locked desktop, because
// the user is still signed in. At the sign-in screen there is no user token
// yet, so fall back to duplicating winlogon.exe's own token from that session.
func consoleSessionToken(session uint32) (windows.Token, error) {
	var user windows.Token
	if err := windows.WTSQueryUserToken(session, &user); err == nil {
		defer user.Close()
		return duplicatePrimary(user, session)
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
