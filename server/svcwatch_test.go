package server

import (
	"strings"
	"testing"

	"github.com/Paco5687/autormm/internal/protocol"
)

// The command is built by concatenating names into a shell string, so anything
// that could end a command and start another must never reach it.
func TestServiceNamesAreFiltered(t *testing.T) {
	bad := []string{
		"sshd; rm -rf /", "a && reboot", "$(reboot)", "`reboot`", "a|b", "a b",
		"a\nreboot", "'", "\"", "", strings.Repeat("x", 65),
	}
	for _, n := range bad {
		if safeServiceName(n) {
			t.Errorf("accepted a dangerous service name: %q", n)
		}
	}
	for _, n := range []string{"sshd", "docker.service", "nginx", "plexmediaserver", "user@1000", "my-svc_2"} {
		if !safeServiceName(n) {
			t.Errorf("rejected a real service name: %q", n)
		}
	}
}

// A rejected name must not silently take the rest of the list with it.
func TestUnsafeNameDoesNotDiscardTheGoodOnes(t *testing.T) {
	_, cmd, ok := serviceStatusCommand("linux", []string{"sshd; reboot", "docker"})
	if !ok {
		t.Fatal("no command built")
	}
	if strings.Contains(cmd, "reboot") {
		t.Errorf("dangerous name reached the command: %q", cmd)
	}
	if !strings.Contains(cmd, "docker") {
		t.Errorf("the safe name was dropped too: %q", cmd)
	}
	// Nothing safe at all means no command rather than one that loops over
	// an empty list and reports every service as stopped.
	if _, _, ok := serviceStatusCommand("linux", []string{"; reboot"}); ok {
		t.Error("built a command from nothing but rejected names")
	}
}

func TestServiceCommandPerPlatform(t *testing.T) {
	for _, os := range []string{"linux", "windows", "darwin"} {
		shell, cmd, ok := serviceStatusCommand(os, []string{"sshd"})
		if !ok || cmd == "" || shell == "" {
			t.Errorf("%s: no command", os)
		}
	}
	if _, _, ok := serviceStatusCommand("plan9", []string{"sshd"}); ok {
		t.Error("claimed support for an unknown platform")
	}
}

func TestParseServiceStates(t *testing.T) {
	got := parseServiceStates("sshd=running\ndocker=stopped\n\nplexmediaserver=running\n")
	if len(got) != 3 {
		t.Fatalf("parsed %d states: %v", len(got), got)
	}
	if !got["sshd"] || got["docker"] || !got["plexmediaserver"] {
		t.Errorf("wrong states: %v", got)
	}
	// A name containing '=' splits on the last one, not the first.
	if s := parseServiceStates("od=d=running"); !s["od=d"] {
		t.Errorf("split on the wrong separator: %v", s)
	}
	// Noise is ignored rather than parsed into a phantom stopped service.
	if s := parseServiceStates("Warning: something\n\n"); len(s) != 0 {
		t.Errorf("parsed noise into states: %v", s)
	}
}

// "Not polled yet" must not read as "everything is down", or every hub restart
// pages for every watched service.
func TestUnknownStateRaisesNothing(t *testing.T) {
	w := newSvcWatcher()
	if got := w.states("nobody"); got != nil {
		t.Errorf("states for an unpolled host = %v, want nil", got)
	}

	a := NewAlerter(AlertConfig{})
	a.svcStates = w.states
	a.watched = func(protocol.HostView) []string { return []string{"sshd"} }
	v := protocol.HostView{AgentID: "h1", Online: true, OS: "linux"}
	for _, r := range a.rulesFor(v) {
		if strings.HasPrefix(r.name, "service:") {
			t.Errorf("raised a service rule with no observation: %+v", r)
		}
	}

	// Once observed as stopped, it does fire.
	w.set("h1", map[string]bool{"sshd": false})
	found := false
	for _, r := range a.rulesFor(v) {
		if r.name == "service:sshd" && r.active {
			found = true
		}
	}
	if !found {
		t.Error("a stopped service raised no rule")
	}
}

// Watch lists union across policies; thresholds do not. A host adding its own
// service must not shed the ones a policy watches everywhere.
func TestWatchedServicesUnionAcrossPolicies(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"all":        {Services: []string{"sshd"}},
		"tag:server": {Services: []string{"docker"}},
		"web1":       {Services: []string{"nginx"}},
	})
	got := p.resolveServices(web1)
	want := map[string]bool{"sshd": true, "docker": true, "nginx": true}
	if len(got) != 3 {
		t.Fatalf("got %v, want all three", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected service %q", n)
		}
	}
}

func TestWatchedServicesDeduplicate(t *testing.T) {
	p := prefsWith(map[string]HostPref{
		"all":  {Services: []string{"docker"}},
		"web1": {Services: []string{"Docker", " docker "}},
	})
	if got := p.resolveServices(web1); len(got) != 1 {
		t.Errorf("got %v, want one entry", got)
	}
}
