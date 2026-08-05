package agent

import "testing"

func TestFirstClaimStopsNobody(t *testing.T) {
	var s screenSessions
	if end := s.claim("a", func() {}); end != nil {
		t.Error("the first session tried to supersede something")
	}
	if s.active() != "a" {
		t.Errorf("active = %q, want a", s.active())
	}
}

// The newest connection wins, and the previous holder is told exactly once.
func TestSecondClaimSupersedesTheFirst(t *testing.T) {
	var s screenSessions
	stopped := 0
	s.claim("a", func() { stopped++ })

	end := s.claim("b", func() {})
	if end == nil {
		t.Fatal("claiming over a live session returned nothing to stop it with")
	}
	if stopped != 0 {
		t.Error("the previous session was stopped before the caller asked")
	}
	end()
	if stopped != 1 {
		t.Errorf("previous session stopped %d times, want 1", stopped)
	}
	if s.active() != "b" {
		t.Errorf("active = %q, want b", s.active())
	}
}

// The dangerous case: a superseded session's deferred release must not evict
// the session that replaced it, or the host would end up with no active
// session recorded and the next claim would not stop the live one.
func TestReleaseFromAnOldSessionDoesNotEvictTheNewOne(t *testing.T) {
	var s screenSessions
	s.claim("a", func() {})
	s.claim("b", func() {})

	s.release("a") // "a" finally unwinds after being superseded
	if s.active() != "b" {
		t.Fatalf("active = %q, want b — the old session evicted the new one", s.active())
	}

	stopped := 0
	s.claim("c", func() {})
	_ = stopped
	if s.active() != "c" {
		t.Errorf("active = %q, want c", s.active())
	}
}

func TestReleaseClearsTheCurrentSession(t *testing.T) {
	var s screenSessions
	s.claim("a", func() {})
	s.release("a")
	if s.active() != "" {
		t.Errorf("active = %q, want empty", s.active())
	}
	if end := s.claim("b", func() {}); end != nil {
		t.Error("claimed after a clean release but still tried to supersede")
	}
}
