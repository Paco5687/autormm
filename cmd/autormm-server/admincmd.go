package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/Paco5687/autormm/internal/adminstore"
	"github.com/Paco5687/autormm/internal/auth"
)

func adminStorePath() string { return filepath.Join(configDir(), "admins.json") }

// runAdminCmd handles `autormm-server admin ...`. Returns true if it handled the
// invocation (the caller should then exit).
func runAdminCmd(args []string) bool {
	if len(args) < 1 || args[0] != "admin" {
		return false
	}
	st := adminstore.New(adminStorePath())
	sub := ""
	if len(args) >= 2 {
		sub = args[1]
	}
	switch sub {
	case "add", "passwd", "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: autormm-server admin add <username>")
			os.Exit(2)
		}
		pw, err := promptNewPassword()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := st.Set(args[2], pw); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("admin %q saved (%s)\n", strings.ToLower(args[2]), adminStorePath())
	case "list", "ls":
		names, err := st.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(names) == 0 {
			fmt.Println("no admin accounts yet — add one with: autormm-server admin add <username>")
		}
		for _, n := range names {
			fmt.Println(n)
		}
	case "rm", "remove", "del":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: autormm-server admin rm <username>")
			os.Exit(2)
		}
		ok, err := st.Remove(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !ok {
			fmt.Printf("no such admin: %s\n", args[2])
			os.Exit(1)
		}
		fmt.Printf("removed admin %q\n", strings.ToLower(args[2]))
	default:
		fmt.Fprintln(os.Stderr, "usage: autormm-server admin <add|list|rm> [username]")
		os.Exit(2)
	}
	return true
}

// runResetCmd handles `autormm-server reset`: it wipes all admin accounts and
// rotates the admin + enrollment tokens. This is the account-recovery path —
// because the enrollment token changes, every host must be re-added.
func runResetCmd(args []string) bool {
	if len(args) < 1 || args[0] != "reset" {
		return false
	}
	yes := false
	for _, a := range args[1:] {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	if !yes {
		fmt.Print("This wipes ALL admin accounts and rotates the enrollment token.\n" +
			"Every host will need to be re-added afterwards.\nType 'reset' to confirm: ")
		var in string
		fmt.Scanln(&in)
		if strings.TrimSpace(in) != "reset" {
			fmt.Println("aborted")
			os.Exit(1)
		}
	}
	admin := auth.RandomID(18)
	enroll := auth.RandomID(18)

	p := loadPersisted()
	p.Admin, p.Enroll = admin, enroll
	savePersisted(p)
	updateServerEnv(admin, enroll)
	os.Remove(filepath.Join(configDir(), "admins.json"))

	fmt.Printf("\nReset complete. New tokens:\n  ADMIN TOKEN:  %s\n  ENROLL TOKEN: %s\n\n", admin, enroll)
	fmt.Println("Next steps:")
	fmt.Println("  1. Restart the hub:   systemctl --user restart autormm-server")
	fmt.Println("  2. Open the dashboard and create a new admin account.")
	fmt.Println("  3. Re-add every host — the previous enrollment token no longer works.")
	return true
}

// updateServerEnv rewrites the token lines in server.env (if the installer
// created one), since the systemd unit sources it and it overrides autormm.json.
func updateServerEnv(admin, enroll string) {
	path := filepath.Join(configDir(), "server.env")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "AUTORMM_ADMIN_TOKEN=") {
			lines[i] = "AUTORMM_ADMIN_TOKEN=" + admin
		} else if strings.HasPrefix(ln, "AUTORMM_ENROLL_TOKEN=") {
			lines[i] = "AUTORMM_ENROLL_TOKEN=" + enroll
		}
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
}

func promptNewPassword() (string, error) {
	fmt.Print("New password: ")
	p1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	fmt.Print("Confirm password: ")
	p2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", fmt.Errorf("passwords do not match")
	}
	if len(p1) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	return string(p1), nil
}
