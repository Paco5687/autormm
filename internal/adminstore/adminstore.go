// Package adminstore persists local admin accounts (username + argon2id hash)
// in a JSON file, shared by the server (login verification) and the
// autormm-server admin CLI. The file is re-read on each operation so CLI
// changes take effect without restarting the hub.
package adminstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Paco5687/autormm/internal/auth"
)

type user struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
}

// Store is a file-backed set of admin accounts.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: path} }

func normalize(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

func (s *Store) load() ([]user, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var us []user
	if err := json.Unmarshal(b, &us); err != nil {
		return nil, err
	}
	return us, nil
}

func (s *Store) save(us []user) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(us, "", "  ")
	return os.WriteFile(s.path, b, 0o600)
}

// Verify reports whether username + password match a stored admin.
func (s *Store) Verify(username, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.load()
	if err != nil {
		return false
	}
	un := normalize(username)
	for _, u := range us {
		if u.Username == un {
			return auth.VerifyPassword(password, u.Hash)
		}
	}
	// Spend comparable time on a miss to blunt username enumeration by timing.
	_, _ = auth.HashPassword(password)
	return false
}

// Set adds a new admin or updates an existing one's password.
func (s *Store) Set(username, password string) error {
	un := normalize(username)
	if un == "" {
		return fmt.Errorf("username required")
	}
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.load()
	if err != nil {
		return err
	}
	for i := range us {
		if us[i].Username == un {
			us[i].Hash = h
			return s.save(us)
		}
	}
	return s.save(append(us, user{Username: un, Hash: h}))
}

// Remove deletes an admin; ok is false if there was no such user.
func (s *Store) Remove(username string) (ok bool, err error) {
	un := normalize(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.load()
	if err != nil {
		return false, err
	}
	out := make([]user, 0, len(us))
	for _, u := range us {
		if u.Username == un {
			ok = true
			continue
		}
		out = append(out, u)
	}
	if !ok {
		return false, nil
	}
	return true, s.save(out)
}

// List returns the admin usernames, sorted.
func (s *Store) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(us))
	for _, u := range us {
		names = append(names, u.Username)
	}
	sort.Strings(names)
	return names, nil
}
