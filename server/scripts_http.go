package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/robfig/cron/v3"
)

func (s *Server) scriptsReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if s.scripts == nil {
		http.Error(w, "scripts require the server to be started with --db", http.StatusConflict)
		return false
	}
	return true
}

func (s *Server) handleScripts(w http.ResponseWriter, r *http.Request) {
	if !s.scriptsReady(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.scripts.ListScripts()
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var sc Script
		if err := json.NewDecoder(r.Body).Decode(&sc); err != nil || sc.Name == "" || sc.Content == "" {
			http.Error(w, "name and content are required", http.StatusBadRequest)
			return
		}
		saved, err := s.scripts.SaveScript(sc)
		if err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		s.scripts.DeleteScript(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScriptRun(w http.ResponseWriter, r *http.Request) {
	if !s.scriptsReady(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ScriptID string `json:"script_id"`
		AgentID  string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ScriptID == "" || req.AgentID == "" {
		http.Error(w, "script_id and agent_id are required", http.StatusBadRequest)
		return
	}
	sc, err := s.scripts.GetScript(req.ScriptID)
	if err != nil {
		http.Error(w, "no such script", http.StatusNotFound)
		return
	}
	run := s.runScript(sc, req.AgentID, "manual")
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.scriptsReady(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.scripts.ListSchedules()
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var sch Schedule
		if err := json.NewDecoder(r.Body).Decode(&sch); err != nil || sch.ScriptID == "" || sch.AgentID == "" || sch.Cron == "" {
			http.Error(w, "script_id, agent_id and cron are required", http.StatusBadRequest)
			return
		}
		if _, err := cron.ParseStandard(sch.Cron); err != nil {
			http.Error(w, "invalid cron expression: "+err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := s.scripts.SaveSchedule(sch)
		if err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		s.scripts.DeleteSchedule(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if !s.scriptsReady(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.scripts.ListRuns(r.URL.Query().Get("agent"), limit)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}
