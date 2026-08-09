package server

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// ---- drafts: list & detail ------------------------------------------------

func (s *Server) handleListDrafts(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	items, err := s.db.ListDraftsForAccount(key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	// Filters: ?q= (title/description), &status=, &tag=, &teamId=
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	status := r.URL.Query().Get("status")
	tag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	teamID := r.URL.Query().Get("teamId")

	ids := make([]string, 0, len(items))
	filtered := make([]db.DraftListItem, 0, len(items))
	for _, it := range items {
		if status != "" && it.Status != status {
			continue
		}
		if teamID != "" && it.TeamID != teamID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title), q) && !strings.Contains(strings.ToLower(it.Description), q) {
			continue
		}
		ids = append(ids, it.DraftID)
		filtered = append(filtered, it)
	}
	tags, err := s.db.TagsForDrafts(ids)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if tag != "" {
		kept := filtered[:0]
		for _, it := range filtered {
			if containsTag(tags[it.DraftID], tag) {
				kept = append(kept, it)
			}
		}
		filtered = kept
	}
	out := make([]map[string]any, 0, len(filtered))
	for _, it := range filtered {
		out = append(out, s.decorateListItemWithTags(it, tags[it.DraftID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drafts": out})
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func (s *Server) handleDraftDetail(w http.ResponseWriter, r *http.Request) {
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
	versions, err := s.db.ListVersions(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	comments, err := s.db.ListComments(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	tags, err := s.db.DraftTags(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"draft":    s.decorateDraft(draft),
		"versions": decorateVersions(versions),
		"comments": decorateComments(comments),
		"tags":     tags,
	})
}

// handleSetDraftTags replaces a draft's tags (comma-separated or array).
func (s *Server) handleSetDraftTags(w http.ResponseWriter, r *http.Request) {
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	if len(req.Tags) > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Too many tags (max 20)."})
		return
	}
	for _, t := range req.Tags {
		if len(strings.TrimSpace(t)) > 40 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Tags must be 40 characters or fewer."})
			return
		}
	}
	if err := s.db.SetDraftTags(draft.ID, req.Tags); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	tags, err := s.db.DraftTags(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": tags})
}

// ---- comments ----------------------------------------------------------------

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	key, _ := s.optionalAuth(r)
	draft, err := s.db.FindDraft(r.PathValue("draftId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Draft not found."})
		return
	}
	if draft.Visibility == "team" && !(key != nil && s.canAccess(draft, key)) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You don't have access to this draft."})
		return
	}
	comments, err := s.db.ListComments(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "comments": decorateComments(comments)})
}

type commentRequest struct {
	Body          string         `json:"body"`
	Anchor        map[string]any `json:"anchor"`
	VersionNumber *int64         `json:"versionNumber"`
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
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
	if draft.Visibility == "team" && !s.canAccess(draft, key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You don't have access to this draft."})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req commentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	text := cleanTextMax(req.Body, 4000)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Comment body is required."})
		return
	}

	current, err := s.db.GetCurrentVersion(draft.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	versionNumber := int64(1)
	if current != nil {
		versionNumber = current.VersionNumber
	}
	if req.VersionNumber != nil && *req.VersionNumber > 0 {
		versionNumber = *req.VersionNumber
	}

	anchor := sanitizeAnchor(req.Anchor)
	comment, err := s.db.AddComment(db.Comment{
		DraftID:       draft.ID,
		VersionNumber: versionNumber,
		Anchor:        anchor,
		Body:          text,
		Author:        key.AccountName,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}

	// A new comment on a non-approved draft returns it to review.
	if draft.Status != "approved" {
		_ = s.db.SetStatus(draft.ID, "in_review")
		draft.Status = "in_review"
	}

	s.fireWebhooks(webhookEvent{
		Event:         evComment,
		Actor:         key.AccountName,
		Draft:         draft,
		VersionNumber: versionNumber,
		Comment:       &comment,
	})
	s.recordActivity("comment", key.AccountName, "commented: "+truncate(text, 120), draft)
	s.recordMentions(text, key.AccountName, draft)

	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "comment": decorateComment(comment)})
}

// ---- status workflow -----------------------------------------------------------

func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	if !validStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid status \"" + req.Status + "\"."})
		return
	}
	from := draft.Status

	// Approval gate: approving a team draft records the approver; the draft
	// only flips to approved once the team's required count is reached.
	if req.Status == "approved" && draft.TeamID != "" {
		if team, err := s.db.FindTeam(draft.TeamID); err == nil && team.RequiredApprovals > 0 {
			_ = s.db.AddDraftApproval(draft.ID, key.AccountID)
			count, _ := s.db.ApprovalCount(draft.ID)
			if count < team.RequiredApprovals {
				_ = s.db.SetStatus(draft.ID, "in_review")
				draft.Status = "in_review"
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":       true,
					"approved": false,
					"draft":    s.decorateDraft(draft),
				})
				return
			}
		}
	}

	if err := s.db.SetStatus(draft.ID, req.Status); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	draft.Status = req.Status
	s.fireWebhooks(webhookEvent{
		Event:      evStatus,
		Actor:      key.AccountName,
		Draft:      draft,
		FromStatus: from,
		ToStatus:   req.Status,
	})
	s.recordActivity("status", key.AccountName, "moved "+from+" → "+req.Status, draft)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": true, "draft": s.decorateDraft(draft)})
}

func (s *Server) handleDeleteDraft(w http.ResponseWriter, r *http.Request) {
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
	if err := s.db.SoftDelete(draft.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- decoration ---------------------------------------------------------------

func (s *Server) decorateListItem(it db.DraftListItem) map[string]any {
	tags, _ := s.db.DraftTags(it.DraftID)
	return s.decorateListItemWithTags(it, tags)
}

func (s *Server) decorateListItemWithTags(it db.DraftListItem, tags []string) map[string]any {
	return map[string]any{
		"draftId":             it.DraftID,
		"title":               it.Title,
		"description":         it.Description,
		"visibility":          it.Visibility,
		"status":              it.Status,
		"teamId":              nullStr(it.TeamID),
		"repoOrg":             nullStr(it.RepoOrg),
		"repoName":            nullStr(it.RepoName),
		"latestVersionNumber": it.LatestVersionNumber,
		"versionCount":        it.VersionCount,
		"tags":                tags,
		"createdAt":           it.CreatedAt,
		"updatedAt":           it.UpdatedAt,
		"publicUrl":           s.cfg.PublicBaseURL + "/d/" + it.DraftID,
		"rawUrl":              s.cfg.PublicBaseURL + "/d/" + it.DraftID + "/raw",
	}
}

func (s *Server) decorateDraft(d *db.Draft) map[string]any {
	var latest *int64
	if current, err := s.db.GetCurrentVersion(d.ID); err == nil && current != nil {
		n := current.VersionNumber
		latest = &n
	}
	out := map[string]any{
		"draftId":             d.ID,
		"title":               d.Title,
		"description":         d.Description,
		"visibility":          d.Visibility,
		"status":              d.Status,
		"teamId":              nullStr(d.TeamID),
		"repoOrg":             nullStr(d.RepoOrg),
		"repoName":            nullStr(d.RepoName),
		"latestVersionNumber": latest,
		"createdAt":           d.CreatedAt,
		"updatedAt":           d.UpdatedAt,
		"publicUrl":           s.cfg.PublicBaseURL + "/d/" + d.ID,
		"rawUrl":              s.cfg.PublicBaseURL + "/d/" + d.ID + "/raw",
	}
	if d.TeamID != "" {
		if team, err := s.db.FindTeam(d.TeamID); err == nil {
			count, _ := s.db.ApprovalCount(d.ID)
			out["approvals"] = map[string]any{"count": count, "required": team.RequiredApprovals}
		}
	}
	return out
}

func decorateVersions(versions []db.Version) []map[string]any {
	out := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		out = append(out, map[string]any{
			"versionId":        v.ID,
			"versionNumber":    v.VersionNumber,
			"contentHash":      v.ContentHash,
			"fileSize":         v.FileSize,
			"createdAt":        v.CreatedAt,
			"gitBranch":        nullStr(v.GitBranch),
			"gitCommitSha":     nullStr(v.GitCommitSHA),
			"gitCommitSubject": nullStr(v.GitCommitSubject),
			"gitDirty":         v.GitDirty,
			"originalFilename": nullStr(v.OriginalFilename),
			"cliVersion":       nullStr(v.CLIVersion),
			"author":           nullStr(v.Author),
		})
	}
	return out
}

func decorateComments(comments []db.Comment) []map[string]any {
	out := make([]map[string]any, 0, len(comments))
	for _, c := range comments {
		out = append(out, decorateComment(c))
	}
	return out
}

func decorateComment(c db.Comment) map[string]any {
	return map[string]any{
		"id":            c.ID,
		"draftId":       c.DraftID,
		"versionNumber": c.VersionNumber,
		"anchor":        parseAnchor(c.Anchor),
		"body":          c.Body,
		"author":        c.Author,
		"createdAt":     c.CreatedAt,
	}
}

// sanitizeAnchor whitelists selector/x/y/note from the client.
func sanitizeAnchor(a map[string]any) string {
	if a == nil {
		return ""
	}
	out := map[string]any{}
	if sel, ok := a["selector"].(string); ok && len(sel) <= 500 && sel != "" {
		out["selector"] = sel
	}
	if x, ok := a["x"].(float64); ok && !math.IsNaN(x) && !math.IsInf(x, 0) {
		out["x"] = int64(x)
	}
	if y, ok := a["y"].(float64); ok && !math.IsNaN(y) && !math.IsInf(y, 0) {
		out["y"] = int64(y)
	}
	if note, ok := a["note"].(string); ok && len(note) <= 200 && note != "" {
		out["note"] = note
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseAnchor(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
