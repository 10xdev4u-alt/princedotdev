package server

import (
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// canAssignReviewers reports whether the actor may change a draft's
// reviewers: the draft owner, or a team owner/admin for team drafts.
func (s *Server) canAssignReviewers(draft *db.Draft, key *db.APIKey) bool {
	if key == nil {
		return false
	}
	if draft.AccountID == key.AccountID {
		return true
	}
	if draft.TeamID == "" {
		return false
	}
	ok, _ := s.db.IsTeamAdmin(draft.TeamID, key.AccountID)
	return ok
}

// handleGetReviewers lists the draft's assigned reviewers.
func (s *Server) handleGetReviewers(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	draft, err := s.db.FindDraft(r.PathValue("draftId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Draft not found."})
		return
	}
	if !s.canAccess(draft, key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You don't have access to this draft."})
		return
	}
	reviewers, err := s.db.ListDraftReviewers(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	status, _ := s.db.ReviewerApprovalStatus(draft.ID)
	list := make([]map[string]any, 0, len(reviewers))
	for _, rv := range reviewers {
		list = append(list, map[string]any{
			"accountId": rv.AccountID,
			"name":      rv.Name,
			"email":     rv.Email,
			"approved":  status[rv.AccountID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reviewers": list})
}

// handleSetReviewers replaces the draft's assigned reviewers.
func (s *Server) handleSetReviewers(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	draft, err := s.db.FindDraft(r.PathValue("draftId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Draft not found."})
		return
	}
	if !s.canAssignReviewers(draft, key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only the draft owner or a team owner/admin can assign reviewers."})
		return
	}
	var req struct {
		AccountIDs []string `json:"accountIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range req.AccountIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		// Reviewers must be real team members (team drafts) or the owner
		// themselves (personal drafts).
		if draft.TeamID != "" {
			if ok, _ := s.db.IsTeamMember(draft.TeamID, id); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Reviewers must be members of the draft's team."})
				return
			}
		} else if id != draft.AccountID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Only the draft owner can review a personal draft."})
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if err := s.db.SetDraftReviewers(draft.ID, ids, key.AccountID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	names := make([]string, 0, len(ids))
	if len(ids) > 0 {
		reviewers, _ := s.db.ListDraftReviewers(draft.ID)
		for _, rv := range reviewers {
			names = append(names, rv.Name)
		}
	}
	s.recordActivity("reviewers", key.AccountName, "assigned reviewers: "+strings.Join(names, ", "), draft)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reviewers": ids})
}
