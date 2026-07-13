package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckMagic(t *testing.T) {
	elf := writeTemp(t, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 16)...))
	pe := writeTemp(t, append([]byte{'M', 'Z'}, make([]byte, 16)...))
	junk := writeTemp(t, []byte("#!/bin/sh\necho hi\n"))

	if err := checkMagic(elf); err != nil {
		t.Errorf("ELF should pass: %v", err)
	}
	if err := checkMagic(pe); err != nil {
		t.Errorf("PE should pass: %v", err)
	}
	if err := checkMagic(junk); err == nil {
		t.Error("shell script should fail magic check")
	}
}

func TestValidateRejectsTiny(t *testing.T) {
	small := writeTemp(t, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 100)...))
	if err := validate(small, "1.2.3"); err == nil {
		t.Error("a tiny binary should be rejected before the smoke test")
	}
}
