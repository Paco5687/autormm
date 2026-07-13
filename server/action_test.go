package server

import "testing"

func TestBuildActionCommand(t *testing.T) {
	cases := []struct {
		name    string
		os      string
		req     actionRequest
		wantCmd string
		wantErr bool
	}{
		{"linux kill", "linux", actionRequest{Kind: "proc", Action: "kill", PID: 123}, "kill -TERM 123", false},
		{"linux force", "linux", actionRequest{Kind: "proc", Action: "force", PID: 123}, "kill -KILL 123", false},
		{"win kill", "windows", actionRequest{Kind: "proc", Action: "kill", PID: 42}, "taskkill /PID 42", false},
		{"win force", "windows", actionRequest{Kind: "proc", Action: "force", PID: 42}, "taskkill /F /PID 42", false},
		{"bad pid", "linux", actionRequest{Kind: "proc", Action: "kill", PID: 0}, "", true},
		{"linux svc", "linux", actionRequest{Kind: "service", Action: "restart", Service: "sshd"}, "systemctl restart sshd", false},
		{"win svc", "windows", actionRequest{Kind: "service", Action: "start", Service: "spooler"}, "Start-Service -Name 'spooler'", false},
		{"svc injection", "linux", actionRequest{Kind: "service", Action: "restart", Service: "x; rm -rf /"}, "", true},
		{"bad svc action", "linux", actionRequest{Kind: "service", Action: "nuke", Service: "sshd"}, "", true},
		{"unknown kind", "linux", actionRequest{Kind: "bogus"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, _, err := buildActionCommand(c.os, c.req)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got cmd %q", cmd)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, c.wantCmd)
			}
		})
	}
}
