package server

import (
	"net/http"
	"strconv"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// handleAudit returns the audit trail. With teamId: that team's entries
// (owners/admins only). Without: the caller's own entries.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	teamID := r.URL.Query().Get("teamId")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if teamID != "" {
		ok, _ := s.db.IsTeamAdmin(teamID, key.AccountID)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only team owners/admins can view the team audit log."})
			return
		}
	}
	entries, err := s.db.ListAudit(teamID, key.AccountID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": entries})
}

// handleTeamStorage returns per-team storage usage (admin/owner only).
func (s *Server) handleTeamStorage(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	teams, err := s.db.ListTeamsForAccount(key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	all, err := s.db.TeamStorageUsage()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	// Only include teams the caller can manage (owner/admin).
	allowed := map[string]bool{}
	for _, t := range teams {
		if ok, _ := s.db.IsTeamAdmin(t.ID, key.AccountID); ok {
			allowed[t.ID] = true
		}
	}
	out := make([]db.TeamStorage, 0, len(all))
	for _, ts := range all {
		if allowed[ts.TeamID] {
			out = append(out, ts)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "teams": out, "storageBudget": s.cfg.StorageBudget})
}
