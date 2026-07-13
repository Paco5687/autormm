//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "autormm-agent"
)

// ensureAutostart writes an HKCU Run entry so this tray app launches at logon,
// with the same arguments this process was started with. Per-user, no admin.
func ensureAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := quoteArg(exe)
	for _, a := range os.Args[1:] {
		cmd += " " + quoteArg(a)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if existing, _, err := k.GetStringValue(runValue); err == nil && existing == cmd {
		return nil // already up to date
	}
	return k.SetStringValue(runValue, cmd)
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
