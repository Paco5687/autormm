package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// collect wraps listDir so tests read its reply directly.
func list(t *testing.T, path string) fileCtrl {
	t.Helper()
	var got fileCtrl
	err := listDir(func(v any) error {
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, &got)
	}, path)
	if err != nil {
		t.Fatalf("listDir(%q): %v", path, err)
	}
	return got
}

func TestListDirOrdersDirsFirst(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("xy"), 0o644)
	os.Mkdir(filepath.Join(dir, "zdir"), 0o755)

	got := list(t, dir)
	if got.Path != dir {
		t.Errorf("resolved path = %q, want %q", got.Path, dir)
	}
	names := []string{}
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	// zdir sorts after both files alphabetically, but directories come first —
	// the order every file manager uses.
	want := []string{"zdir", "a.txt", "b.txt"}
	for i, w := range want {
		if i >= len(names) || names[i] != w {
			t.Fatalf("order = %v, want %v", names, want)
		}
	}
	if got.Entries[0].Dir != true || got.Entries[1].Size != 2 {
		t.Errorf("entry metadata wrong: %+v", got.Entries)
	}
}

func TestListDirEmptyPathIsHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := list(t, ""); got.Path != home {
		t.Errorf("empty path listed %q, want home %q", got.Path, home)
	}
}

func TestListDirParentStopsAtRoot(t *testing.T) {
	got := list(t, "/")
	if got.Parent != "" {
		t.Errorf("root's parent = %q, want none — an Up button that loops on / forever looks broken", got.Parent)
	}
}

func TestListDirRejectsFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plain.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	err := listDir(func(any) error { return nil }, f)
	if err == nil {
		t.Error("listing a plain file should error, not pretend it is empty")
	}
}
