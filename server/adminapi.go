package server

import (
	"encoding/json"
	"net/http"
)

// handleAdminAccounts lists admin usernames (admin-gated).
func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.admins == nil {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": []string{}})
		return
	}
	names, _ := s.admins.List()
	writeJSON(w, http.StatusOK, map[string]any{"accounts": names})
}

// handleAdminSet creates or changes an admin's password. Because it's admin-
// gated, holding the admin token is enough to set a password — no reset needed.
func (s *Server) handleAdminSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.admins == nil {
		http.Error(w, "account storage unavailable", http.StatusNotImplemented)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.admins.Set(req.Username, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminRemove deletes an admin account (admin-gated).
func (s *Server) handleAdminRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.admins == nil {
		http.Error(w, "account storage unavailable", http.StatusNotImplemented)
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ok, err := s.admins.Remove(req.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}
