package agent

import (
	"log"
	"sync"
)

// screenSessions enforces one screen session per host.
//
// Each session is a whole capture + encode pipeline. A second one doubles the
// host's CPU and halves the bitrate available to each, usually for a picture
// nobody is looking at — a tab left open, a phone still connected from this
// morning. The newest connection wins, which matches what an operator expects
// when they click Remote.
//
// Terminal and file sessions are deliberately not covered: several of those at
// once are cheap and genuinely useful.
type screenSessions struct {
	mu        sync.Mutex
	id        string
	supersede func() // tell the holder it has been replaced, then stop it
}

// claim makes id the active screen session and returns a function that ends the
// previous holder, or nil if there was none. The caller runs it *before* doing
// its own expensive setup, so the two pipelines never overlap.
func (s *screenSessions) claim(id string, supersede func()) func() {
	s.mu.Lock()
	prev, prevID := s.supersede, s.id
	s.id, s.supersede = id, supersede
	s.mu.Unlock()

	if prev == nil {
		return nil
	}
	return func() {
		log.Printf("session %s: superseding screen session %s (one at a time)", id, prevID)
		prev()
	}
}

// release clears the active session if it is still id. A newer session may have
// claimed it already, in which case this one must not clear the newcomer.
func (s *screenSessions) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id == id {
		s.id, s.supersede = "", nil
	}
}

// active reports the current screen session id, for tests and logging.
func (s *screenSessions) active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
