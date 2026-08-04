package agent

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// decode mirrors what powershell.exe does with -EncodedCommand, so a mistake in
// the encoder (byte order in particular) fails here rather than silently
// breaking every Windows command in the field.
func decodeEncodedCommand(t *testing.T, enc string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16 payload has an odd byte count (%d)", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8) // little-endian
	}
	return string(utf16.Decode(units))
}

func TestEncodePowerShellRoundTrips(t *testing.T) {
	for _, script := range []string{
		"Get-Process",
		"Write-Output 'hello world'",
		// The shapes that -Command mangles: newlines, both quote styles, and
		// the operators PowerShell and cmd fight over.
		"$ErrorActionPreference = 'Stop'\nif ($true) { Write-Output \"quoted & piped | here\" }\n",
		"& $dest -version 2>$null | Select-Object -First 1",
		"Write-Output ('a' + $x)  # trailing comment",
		"Write-Output 'ünïcödé — em dash'",
	} {
		if got := decodeEncodedCommand(t, encodePowerShell(script)); got != script {
			t.Errorf("round-trip changed the script:\n  in:  %q\n  out: %q", script, got)
		}
	}
}

// The real installer scripts must survive intact — they are the reason this
// exists.
func TestShellForPowerShellUsesEncodedCommand(t *testing.T) {
	script := "$ErrorActionPreference = 'Stop'\nWrite-Output 'multi\nline'\n"
	name, args := shellFor("powershell", script)
	if name != "powershell" {
		t.Fatalf("shell = %q, want powershell", name)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-Command") {
		t.Error("still using -Command, which re-parses the command line")
	}
	var enc string
	for i, a := range args {
		if a == "-EncodedCommand" && i+1 < len(args) {
			enc = args[i+1]
		}
	}
	if enc == "" {
		t.Fatal("no -EncodedCommand payload")
	}
	if got := decodeEncodedCommand(t, enc); got != script {
		t.Errorf("script did not survive:\n  in:  %q\n  out: %q", script, got)
	}
	// The raw script must not also appear as a bare argument.
	if strings.Contains(joined, "Write-Output") {
		t.Error("the plaintext script is still on the command line")
	}
}

// Non-Windows shells must be untouched.
func TestShellForPosixUnchanged(t *testing.T) {
	name, args := shellFor("sh", "echo hi")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" {
		t.Errorf("sh handling changed: %q %v", name, args)
	}
}
