package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/Paco5687/autormm/internal/adminstore"
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
