package agent

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

const inventoryMax = 5000

// runInventory enumerates installed software and returns it to the server.
func (a *Agent) runInventory(parent context.Context, out chan<- any, req protocol.InventoryRequest) {
	source, pkgs, err := collectInventory(parent)
	resp := protocol.InventoryResponse{
		Type: protocol.TypeInventoryResp, ReqID: req.ReqID,
		Source: source, Packages: pkgs,
	}
	if err != nil {
		resp.Err = err.Error()
	}
	select {
	case out <- resp:
	case <-parent.Done():
	}
}

func collectInventory(parent context.Context) (string, []protocol.Package, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	run := func(name string, args ...string) (string, bool) {
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}

	switch runtime.GOOS {
	case "linux":
		if out, ok := run("dpkg-query", "-W", "-f", "${Package}\t${Version}\n"); ok {
			return "dpkg", parseTabbed(out), nil
		}
		if out, ok := run("rpm", "-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\n"); ok {
			return "rpm", parseTabbed(out), nil
		}
		return "", nil, fmt.Errorf("no supported package manager (dpkg/rpm) found")
	case "windows":
		ps := `Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*, ` +
			`HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\* ` +
			`| Where-Object { $_.DisplayName } ` +
			`| ForEach-Object { "$($_.DisplayName)` + "`t" + `$($_.DisplayVersion)" }`
		if out, ok := run("powershell", "-NoProfile", "-NonInteractive", "-Command", ps); ok {
			return "windows", parseTabbed(out), nil
		}
		return "", nil, fmt.Errorf("could not query installed programs")
	case "darwin":
		if out, ok := run("brew", "list", "--versions"); ok {
			return "brew", parseSpaced(out), nil
		}
		return "", nil, fmt.Errorf("no supported inventory source (brew) found")
	default:
		return "", nil, fmt.Errorf("inventory not supported on %s", runtime.GOOS)
	}
}

func parseTabbed(out string) []protocol.Package {
	var pkgs []protocol.Package
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, ver, _ := strings.Cut(line, "\t")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		pkgs = append(pkgs, protocol.Package{Name: name, Version: strings.TrimSpace(ver)})
		if len(pkgs) >= inventoryMax {
			break
		}
	}
	return pkgs
}

func parseSpaced(out string) []protocol.Package {
	var pkgs []protocol.Package
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ver := ""
		if len(fields) > 1 {
			ver = fields[1]
		}
		pkgs = append(pkgs, protocol.Package{Name: fields[0], Version: ver})
		if len(pkgs) >= inventoryMax {
			break
		}
	}
	return pkgs
}
