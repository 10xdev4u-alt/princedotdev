package server

import (
	"net/http"
	"strconv"

	"github.com/10xdev4u-alt/princedotdev/internal/diff"
)

// handleDraftDiff returns a line diff between two versions of a draft.
// Query params: from (default 1) and to (default latest).
func (s *Server) handleDraftDiff(w http.ResponseWriter, r *http.Request) {
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

	from := int64(1)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "from must be a positive version number."})
			return
		}
		from = n
	}
	to := int64(0)
	if v := r.URL.Query().Get("to"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "to must be a positive version number."})
			return
		}
		to = n
	}
	if to == 0 {
		if current, err := s.db.GetCurrentVersion(draft.ID); err == nil && current != nil {
			to = current.VersionNumber
		}
	}
	if to == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Draft has no versions."})
		return
	}
	if from == to {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Choose two different versions to compare."})
		return
	}
	if from > to {
		from, to = to, from
	}

	vFrom, err := s.db.GetVersion(draft.ID, from)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	vTo, err := s.db.GetVersion(draft.ID, to)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if vFrom == nil || vTo == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Version not found."})
		return
	}

	oldHTML, err := s.store.Get(vFrom.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Could not read version content."})
		return
	}
	newHTML, err := s.store.Get(vTo.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Could not read version content."})
		return
	}

	hunks := diff.Lines(string(oldHTML), string(newHTML))
	added, removed := diff.Counts(hunks)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"draftId": draft.ID,
		"from":    from,
		"to":      to,
		"fromVersion": map[string]any{
			"versionNumber": vFrom.VersionNumber,
			"createdAt":     vFrom.CreatedAt,
			"fileSize":      vFrom.FileSize,
		},
		"toVersion": map[string]any{
			"versionNumber": vTo.VersionNumber,
			"createdAt":     vTo.CreatedAt,
			"fileSize":      vTo.FileSize,
		},
		"stats": map[string]any{"added": added, "removed": removed},
		"hunks": hunks,
	})
}
