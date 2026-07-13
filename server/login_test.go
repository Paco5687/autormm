package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Paco5687/autormm/internal/adminstore"
)

func TestPasswordLogin(t *testing.T) {
	dir := t.TempDir()
	adminsPath := filepath.Join(dir, "admins.json")
	if err := adminstore.New(adminsPath).Set("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SecretPhrase: "test-secret", AdminStore: adminsPath})

	login := func(user, pass string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		s.handleLogin(w, r)
		return w
	}

	// wrong password -> 401
	if w := login("alice", "nope"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: got %d, want 401", w.Code)
	}

	// correct -> token that authorises admin API
	w := login("alice", "correct horse")
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d, want 200", w.Code)
	}
	var resp struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Token == "" {
		t.Fatal("expected a session token")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/hosts", nil)
	r.Header.Set("Authorization", "Bearer "+resp.Token)
	if !s.checkAdmin(r) {
		t.Error("login token should authorise admin access")
	}

	// a random token must not authorise
	r2 := httptest.NewRequest(http.MethodGet, "/api/hosts", nil)
	r2.Header.Set("Authorization", "Bearer not-a-real-token")
	if s.checkAdmin(r2) {
		t.Error("garbage token must not authorise")
	}
}
