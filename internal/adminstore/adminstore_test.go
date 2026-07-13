package adminstore

import (
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "admins.json"))

	if st.Verify("alice", "secret123") {
		t.Fatal("verify should fail on an empty store")
	}
	if err := st.Set("Alice", "secret123"); err != nil {
		t.Fatal(err)
	}
	if !st.Verify("alice", "secret123") { // usernames are case-insensitive
		t.Error("correct password should verify")
	}
	if st.Verify("alice", "wrong") {
		t.Error("wrong password must not verify")
	}
	if err := st.Set("x", "short"); err == nil {
		t.Error("should reject a password under 8 chars")
	}

	// update password
	if err := st.Set("alice", "newpassword"); err != nil {
		t.Fatal(err)
	}
	if st.Verify("alice", "secret123") || !st.Verify("alice", "newpassword") {
		t.Error("password update did not take effect")
	}

	names, _ := st.List()
	if len(names) != 1 || names[0] != "alice" {
		t.Errorf("List = %v, want [alice]", names)
	}
	if ok, _ := st.Remove("alice"); !ok {
		t.Error("remove should report success")
	}
	if st.Verify("alice", "newpassword") {
		t.Error("removed user must not verify")
	}
}
