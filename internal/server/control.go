package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleStats reports instance-wide totals for the storage meter.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.Stats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"stats":         stats,
		"storageBudget": s.cfg.StorageBudget,
	})
}

// handleMintKey creates a new API key for the authenticated account.
func (s *Server) handleMintKey(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	name := cleanTextMax(req.Name, 100)
	if name == "" {
		name = "API Key · " + time.Now().UTC().Format("2006-01-02")
	}
	id, token, err := s.db.CreateAPIKey(key.AccountID, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "apiKey": map[string]any{"id": id, "name": name}, "token": token})
}

// handleRevokeKey revokes one of the authenticated account's keys.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	if err := s.db.RevokeAPIKey(r.PathValue("keyId"), key.AccountID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListTeamMembers lists members of a team the caller belongs to.
func (s *Server) handleListTeamMembers(w http.ResponseWriter, r *http.Request) {
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
	members, err := s.db.ListTeamMembers(team.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members})
}

// handleRemoveTeamMember removes a member (owner-only; the owner cannot be
// removed). The account itself cannot be removed by the team action.
func (s *Server) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only team owners can remove members."})
		return
	}
	accountID := r.PathValue("accountId")
	if strings.TrimSpace(accountID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Member accountId is required."})
		return
	}
	if err := s.db.RemoveTeamMember(team.ID, accountID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
