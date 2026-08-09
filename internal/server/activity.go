package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// recordActivity appends a feed entry scoped to a draft's owner + team.
// recordAudit appends an immutable audit entry (control-panel trail).
func (s *Server) recordAudit(accountID, teamID, actor, action, target, details string) {
	_ = s.db.RecordAudit(db.AuditEntry{
		AccountID: accountID,
		TeamID:    teamID,
		Actor:     actor,
		Action:    action,
		Target:    target,
		Details:   details,
	})
}

func (s *Server) recordActivity(kind, actor, body string, draft *db.Draft) {
	if draft == nil {
		return
	}
	_ = s.db.AddActivity(db.Activity{
		AccountID: draft.AccountID,
		TeamID:    draft.TeamID,
		DraftID:   draft.ID,
		Kind:      kind,
		Actor:     actor,
		Body:      body,
	})
}

// recordTeamActivity appends a team-scoped entry (no draft).
func (s *Server) recordTeamActivity(kind, actor, body, teamID string) {
	_ = s.db.AddActivity(db.Activity{TeamID: teamID, Kind: kind, Actor: actor, Body: body})
}

var mentionRe = regexp.MustCompile(`@([a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})`)

// recordMentions scans a comment body for @email mentions and writes a
// personal feed entry for each matching account.
func (s *Server) recordMentions(commentBody, actor string, draft *db.Draft) {
	if draft == nil {
		return
	}
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(commentBody, -1) {
		email := strings.ToLower(m[1])
		if seen[email] {
			continue
		}
		seen[email] = true
		acct, err := s.db.FindAccountByEmail(email)
		if err != nil || acct == nil {
			continue
		}
		_ = s.db.AddActivity(db.Activity{
			AccountID: acct.ID,
			DraftID:   draft.ID,
			Kind:      "mention",
			Actor:     actor,
			Body:      "mentioned you in \"" + truncate(draft.Title, 80) + "\"",
		})
	}
}

// handleActivity returns the account's feed with an unread count.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	items, err := s.db.ListActivity(key.AccountID, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	unread, err := s.db.UnreadActivity(key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "activity": items, "unread": unread})
}

// handleMarkActivityRead clears the unread marker.
func (s *Server) handleMarkActivityRead(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	if err := s.db.MarkActivityRead(key.AccountID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
