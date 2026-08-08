package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	teams, err := s.db.ListTeamsForAccount(key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "teams": teams})
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	name := cleanTextMax(req.Name, 100)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Team name is required."})
		return
	}
	team, err := s.db.CreateTeam(name, key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "team": team})
}

func (s *Server) handleTeamDetail(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	team, err := s.db.FindTeam(r.PathValue("teamId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if team.ID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Team not found."})
		return
	}
	member, err := s.db.IsTeamMember(team.ID, key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if !member {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You're not a member of that team."})
		return
	}
	drafts, err := s.db.ListTeamDrafts(team.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	out := make([]map[string]any, 0, len(drafts))
	for _, it := range drafts {
		out = append(out, s.decorateListItem(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": team, "drafts": out})
}

func (s *Server) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	team, err := s.db.FindTeam(r.PathValue("teamId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if team.ID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Team not found."})
		return
	}
	owner, err := s.db.IsTeamOwner(team.ID, key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if !owner {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only team owners can add members."})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	email := strings.ToLower(cleanTextMax(req.Email, 200))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Member email is required."})
		return
	}
	account, err := s.db.FindAccountByEmail(email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if account == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "No account with that email yet — they must create one first (create-user)."})
		return
	}
	if err := s.db.AddTeamMember(team.ID, account.ID, "member"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":     true,
		"member": map[string]any{"accountId": account.ID, "name": account.Name, "email": account.Email},
	})
}
