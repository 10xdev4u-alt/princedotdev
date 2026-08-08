package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/policy"
)

const (
	maxJSONBytes  = 2 << 20 // 2 MiB request body budget for /api
	versionIDPref = "ver_"
)

var validStatuses = map[string]bool{"draft": true, "in_review": true, "changes_requested": true, "approved": true}
var validVisibilities = map[string]bool{"public": true, "unlisted": true, "team": true}

// uploadRequest mirrors the CLI/agent POST body.
type uploadRequest struct {
	HTML        string     `json:"html"`
	Filename    string     `json:"filename"`
	DraftID     string     `json:"draftId"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	TeamID      string     `json:"teamId"`
	Metadata    uploadMeta `json:"metadata"`
}

type uploadMeta struct {
	CLIVersion    string `json:"cliVersion"`
	GitBranch     string `json:"gitBranch"`
	GitCommitSHA  string `json:"gitCommitSha"`
	GitCommitSubj string `json:"gitCommitSubject"`
	GitDirty      *bool  `json:"gitDirty"`
	OriginalFile  string `json:"originalFilename"`
}

// handleUpload validates, stores, and versions one HTML draft.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read request body."})
		return
	}
	var req uploadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}

	val := policy.Validate([]byte(req.HTML), int(s.cfg.MaxHTMLBytes))
	if !val.OK {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "errors": val.Errors, "warnings": val.Warnings})
		return
	}

	key := authFrom(r)
	anonymous := key == nil
	accountID := ""
	if key != nil {
		accountID = key.AccountID
	}

	requestedVis := req.Visibility
	if requestedVis == "" {
		if req.TeamID != "" {
			requestedVis = "team"
		} else {
			requestedVis = "unlisted"
		}
	}
	if !validVisibilities[requestedVis] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid visibility \"" + requestedVis + "\"."})
		return
	}
	if requestedVis == "team" && anonymous {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "Team drafts require an API key. Run: draftdeck auth set <api-key>",
		})
		return
	}
	if req.TeamID != "" {
		member, err := s.db.IsTeamMember(req.TeamID, accountID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		if !member {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You're not a member of that team."})
			return
		}
	}

	title := val.Title
	if title == "" {
		title = cleanText(req.Filename)
	}
	if title == "" {
		title = "Untitled Draft"
	}
	description := cleanTextMax(req.Description, 1000)

	var draft *db.Draft
	created := false
	if req.DraftID != "" {
		existing, err := s.db.FindDraft(req.DraftID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		if existing == nil || existing.DeletedAt != "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Draft not found."})
			return
		}
		if !s.canAccess(existing, key) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You don't have access to this draft."})
			return
		}
		draft = existing
		// A new version after approval means the agent incorporated feedback:
		// reset the status so the review cycle starts fresh (persisted).
		if draft.Status == "approved" {
			_ = s.db.SetStatus(draft.ID, "draft")
			draft.Status = "draft"
		}
	} else {
		owner := accountID
		if anonymous {
			owner = db.AnonymousAccountID
		}
		newID, err := s.db.CreateDraft(owner, req.TeamID, title, description, requestedVis)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		draft, err = s.db.FindDraft(newID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		created = true
	}

	// Storage budget guard: reject uploads that would push stored HTML past
	// the configured budget (default 5 GiB) before touching disk or DB.
	stored, err := s.db.SumStoredBytes()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if stored+int64(len(req.HTML)) > s.cfg.StorageBudget {
		writeJSON(w, http.StatusInsufficientStorage, map[string]any{
			"ok":    false,
			"error": "Storage budget exceeded. Delete old drafts or raise STORAGE_BUDGET_BYTES.",
		})
		return
	}

	versionID := versionIDPref + randomID(20)
	objectKey := "drafts/" + draft.ID + "/versions/" + versionID + ".html"
	if err := s.store.Put(objectKey, []byte(req.HTML)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}

	byKey := db.AnonymousKeyID
	if key != nil {
		byKey = key.ID
	}
	v, err := s.db.AddVersion(db.Version{
		DraftID:          draft.ID,
		ObjectKey:        objectKey,
		ContentHash:      sha256Hex(req.HTML),
		FileSize:         int64(len(req.HTML)),
		GitBranch:        cleanText(req.Metadata.GitBranch),
		GitCommitSHA:     cleanText(req.Metadata.GitCommitSHA),
		GitCommitSubject: cleanText(req.Metadata.GitCommitSubj),
		GitDirty:         req.Metadata.GitDirty != nil && *req.Metadata.GitDirty,
		OriginalFilename: cleanText(req.Filename),
		CLIVersion:       cleanText(req.Metadata.CLIVersion),
	}, byKey, clientIP(r), r.UserAgent())
	if err != nil {
		_ = s.store.Delete(objectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}

	if err := s.db.SetCurrentVersion(draft.ID, v.ID, title, description); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"ok":            true,
		"draftId":       draft.ID,
		"versionId":     v.ID,
		"versionNumber": v.VersionNumber,
		"title":         draft.Title,
		"visibility":    draft.Visibility,
		"status":        draft.Status,
		"publicUrl":     s.cfg.PublicBaseURL + "/d/" + draft.ID,
		"rawUrl":        s.cfg.PublicBaseURL + "/d/" + draft.ID + "/raw",
		"warnings":      val.Warnings,
	})
}

// canAccess reports whether key may view/edit a draft: the owning account, or
// any member of the draft's team. Anonymous (nil) keys get access only to
// drafts owned by the anonymous sentinel (their own anonymous uploads).
func (s *Server) canAccess(draft *db.Draft, key *db.APIKey) bool {
	if key == nil {
		return draft.AccountID == db.AnonymousAccountID && draft.Visibility != "team"
	}
	if draft.AccountID != "" && draft.AccountID == key.AccountID {
		return true
	}
	if draft.TeamID != "" {
		member, err := s.db.IsTeamMember(draft.TeamID, key.AccountID)
		return err == nil && member
	}
	return false
}

// handleServe returns the raw HTML contract handler: the exact uploaded bytes
// to every client, with a hard CSP and draft metadata headers.
func (s *Server) handleServe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		draftID := r.PathValue("draftId")
		draft, err := s.db.FindDraft(draftID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		if draft == nil || draft.DeletedAt != "" {
			http.NotFound(w, r)
			return
		}

		var version *db.Version
		if vn := r.PathValue("versionNumber"); vn != "" {
			n, perr := strconv.ParseInt(vn, 10, 64)
			if perr != nil || n < 1 {
				http.NotFound(w, r)
				return
			}
			version, err = s.db.GetVersion(draft.ID, n)
		} else {
			version, err = s.db.GetCurrentVersion(draft.ID)
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		if version == nil {
			http.NotFound(w, r)
			return
		}

		if draft.Visibility == "team" {
			key, _ := s.optionalAuth(r)
			if key == nil || !s.canAccess(draft, key) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(needAuthHTML))
				return
			}
		}

		html, err := s.store.Get(version.ObjectKey)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		w.Header().Set("Content-Security-Policy", draftCSP())
		w.Header().Set("X-Draftdeck-Draft-Id", draft.ID)
		w.Header().Set("X-Draftdeck-Draft-Version", itoa64(version.VersionNumber))
		w.Header().Set("X-Draftdeck-Draft-Status", draft.Status)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	}
}

const needAuthHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Team draft</title>
<style>body{font-family:ui-sans-serif,system-ui,sans-serif;background:#f8fafc;color:#111827;display:grid;place-items:center;min-height:100vh;margin:0}p{color:#6b7280;text-align:center;max-width:420px;line-height:1.6}</style>
</head><body><main><h1>Private team draft</h1><p>This draft is visible only to members of its team. Sign in with an API key to view it.</p></main></body></html>`

func draftCSP() string {
	return "default-src 'none'; script-src 'none'; script-src-attr 'none'; style-src 'unsafe-inline'; img-src https: data:; connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
}

// ---- tiny helpers -----------------------------------------------------------

func cleanText(s string) string { return cleanTextMax(s, 255) }

func cleanTextMax(s string, max int) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	if len(t) > max {
		t = t[:max]
	}
	return t
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// randomID returns n URL-safe random chars (mixed case, like the Node ids).
func randomID(n int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
