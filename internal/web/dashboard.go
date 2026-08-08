package web

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// NewDashboard builds the web UI handler. Sessions are disabled when secret
// is empty (no SESSION_SECRET).
func NewDashboard(cfgSecret string, d *db.DB) *DashboardHandler {
	return &DashboardHandler{db: d, secret: cfgSecret}
}

// DashboardHandler serves the web UI. Safe for concurrent ServeHTTP calls.
type DashboardHandler struct {
	db     *db.DB
	secret string
}

// Enabled reports whether sessions are configured (SESSION_SECRET set).
func (h *DashboardHandler) Enabled() bool { return h.secret != "" }

// Routes registers the dashboard routes on mux.
func (h *DashboardHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard", h.handleDashboard)
	mux.HandleFunc("POST /dashboard/session", h.handleSession)
	mux.HandleFunc("POST /auth/sign-out", h.handleSignOut)
	mux.HandleFunc("GET /dashboard/drafts/{draftId}", h.handleDraftPage)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/comments", h.handlePostComment)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/status", h.handlePostStatus)
	mux.HandleFunc("GET /cli/auth", h.handleCLIAuth)
	mux.HandleFunc("POST /cli/auth/keys", h.handleMintKey)
}

// ---- handlers ---------------------------------------------------------------

func (h *DashboardHandler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	session := h.session(r)
	if session == nil {
		h.writeSignIn(w, "")
		return
	}
	items, err := h.db.ListDraftsForAccount(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load drafts.")
		return
	}
	teams, err := h.db.ListTeamsForAccount(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load teams.")
		return
	}
	rows := make([]draftRow, 0, len(items))
	for _, it := range items {
		latest := "—"
		if it.LatestVersionNumber > 0 {
			latest = itoa(it.LatestVersionNumber)
		}
		rows = append(rows, draftRow{
			DraftID:      it.DraftID,
			Title:        it.Title,
			Description:  it.Description,
			Visibility:   it.Visibility,
			Status:       it.Status,
			StatusLabel:  statusLabel(it.Status),
			LatestLabel:  latest,
			VersionCount: it.VersionCount,
			UpdatedLabel: formatDate(it.UpdatedAt),
		})
	}
	body := template.HTML(execOrEmpty(dashboardTpl, map[string]any{
		"Drafts": rows,
		"Teams":  teams,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "My drafts — draftdeck",
		Header: h.header(session, "dashboard"),
		Body:   body,
	})
}

func (h *DashboardHandler) handleSession(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		h.writeError(w, http.StatusServiceUnavailable, "Web sign-in is not configured (set SESSION_SECRET).")
		return
	}
	key := strings.TrimSpace(r.FormValue("apiKey"))
	var auth *db.APIKey
	if key != "" {
		auth, _ = h.db.FindAPIKeyByToken(key)
	}
	if auth == nil {
		h.writeSignIn(w, "That API key was rejected. Generate one with the CLI setup page.")
		return
	}
	cookie, err := CreateSessionCookie(h.secret, Session{
		AccountID:   auth.AccountID,
		AccountName: auth.AccountName,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not create session.")
		return
	}
	w.Header().Add("Set-Cookie", cookie)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (h *DashboardHandler) handleSignOut(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Set-Cookie", ClearSessionCookie())
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (h *DashboardHandler) handleDraftPage(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	draft, err := h.db.FindDraft(r.PathValue("draftId"))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load draft.")
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		h.writeError(w, http.StatusNotFound, "Draft not found.")
		return
	}
	if !h.canView(draft, session) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this draft.")
		return
	}
	versions, err := h.db.ListVersions(draft.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load versions.")
		return
	}
	comments, err := h.db.ListComments(draft.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load comments.")
		return
	}
	vRows := make([]versionRow, 0, len(versions))
	for _, v := range versions {
		vRows = append(vRows, versionRow{
			VersionNumber: v.VersionNumber,
			CommitSubject: v.GitCommitSubject,
			Branch:        v.GitBranch,
			ShortSHA:      shortSHA(v.GitCommitSHA),
			GitDirty:      v.GitDirty,
			CreatedLabel:  formatDate(v.CreatedAt),
		})
	}
	cRows := make([]commentRow, 0, len(comments))
	for _, c := range comments {
		cRows = append(cRows, commentRow{
			ID:            c.ID,
			Author:        c.Author,
			VersionNumber: c.VersionNumber,
			Body:          c.Body,
			AnchorLabel:   anchorLabel(c.Anchor),
			CreatedLabel:  formatDate(c.CreatedAt),
		})
	}
	body := template.HTML(execOrEmpty(draftDetailTpl, map[string]any{
		"DraftID":     draft.ID,
		"Title":       draft.Title,
		"Description": draft.Description,
		"Visibility":  draft.Visibility,
		"Status":      draft.Status,
		"StatusLabel": statusLabel(draft.Status),
		"Versions":    vRows,
		"Comments":    cRows,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  draft.Title + " — draftdeck",
		Header: h.header(session, "dashboard"),
		Body:   body,
	})
}

func (h *DashboardHandler) handlePostComment(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	draftID := r.PathValue("draftId")
	draft, err := h.db.FindDraft(draftID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load draft.")
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		h.writeError(w, http.StatusNotFound, "Draft not found.")
		return
	}
	if !h.canView(draft, session) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this draft.")
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if len(body) > 4000 {
		body = body[:4000]
	}
	if body == "" {
		http.Redirect(w, r, "/dashboard/drafts/"+draftID+"?error=empty-comment", http.StatusFound)
		return
	}
	current, err := h.db.GetCurrentVersion(draft.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load draft.")
		return
	}
	versionNumber := int64(1)
	if current != nil {
		versionNumber = current.VersionNumber
	}
	_, err = h.db.AddComment(db.Comment{
		DraftID:       draft.ID,
		VersionNumber: versionNumber,
		Anchor:        "",
		Body:          body,
		Author:        session.AccountName,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not save comment.")
		return
	}
	if draft.Status != "approved" {
		_ = h.db.SetStatus(draft.ID, "in_review")
	}
	http.Redirect(w, r, "/dashboard/drafts/"+draftID+"#comments", http.StatusFound)
}

func (h *DashboardHandler) handlePostStatus(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	draftID := r.PathValue("draftId")
	draft, err := h.db.FindDraft(draftID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load draft.")
		return
	}
	if draft == nil || draft.DeletedAt != "" {
		h.writeError(w, http.StatusNotFound, "Draft not found.")
		return
	}
	if !h.canView(draft, session) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this draft.")
		return
	}
	status := r.FormValue("status")
	if status == "draft" || status == "in_review" || status == "changes_requested" || status == "approved" {
		_ = h.db.SetStatus(draft.ID, status)
	}
	http.Redirect(w, r, "/dashboard/drafts/"+draftID, http.StatusFound)
}

func (h *DashboardHandler) handleCLIAuth(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	keys, err := h.db.ListAPIKeys(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load keys.")
		return
	}
	type keyRow struct {
		Name          string
		CreatedLabel  string
		LastUsedLabel string
	}
	rows := make([]keyRow, 0, len(keys))
	for _, k := range keys {
		last := "never used"
		if k.LastUsedAt != "" {
			last = formatDate(k.LastUsedAt)
		}
		rows = append(rows, keyRow{Name: k.Name, CreatedLabel: formatDate(k.CreatedAt), LastUsedLabel: last})
	}
	body := template.HTML(execOrEmpty(cliAuthTpl, map[string]any{"Keys": rows}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "CLI setup — draftdeck",
		Header: h.header(session, "cli"),
		Body:   body,
	})
}

func (h *DashboardHandler) handleMintKey(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	name := "CLI · " + time.Now().UTC().Format("2006-01-02")
	_, token, err := h.db.CreateAPIKey(session.AccountID, name)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not mint a key.")
		return
	}
	body := template.HTML(execOrEmpty(cliAuthKeyTpl, map[string]any{
		"Token":   token,
		"KeyName": name,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "Your new API key — draftdeck",
		Header: h.header(session, "cli"),
		Body:   body,
	})
}

// ---- helpers -----------------------------------------------------------------

func (h *DashboardHandler) session(r *http.Request) *Session {
	return ReadSession(r, h.secret)
}

func (h *DashboardHandler) requireSession(w http.ResponseWriter, r *http.Request) *Session {
	session := h.session(r)
	if session == nil {
		h.writeSignIn(w, "Sign in to continue.")
		return nil
	}
	return session
}

func (h *DashboardHandler) canView(draft *db.Draft, session *Session) bool {
	if draft.AccountID != "" && draft.AccountID == session.AccountID {
		return true
	}
	if draft.TeamID != "" {
		member, err := h.db.IsTeamMember(draft.TeamID, session.AccountID)
		return err == nil && member
	}
	return false
}

func (h *DashboardHandler) header(session *Session, active string) template.HTML {
	return template.HTML(execOrEmpty(headerTpl, map[string]any{
		"AccountName": session.AccountName,
		"Active":      active,
	}))
}

func (h *DashboardHandler) writeSignIn(w http.ResponseWriter, errMsg string) {
	body := template.HTML(execOrEmpty(signInTpl, map[string]any{"Error": errMsg}))
	h.writePage(w, http.StatusOK, Page{Title: "Sign in — draftdeck", Body: body})
}

func (h *DashboardHandler) writeError(w http.ResponseWriter, status int, message string) {
	body := template.HTML(execOrEmpty(errorTpl, map[string]any{"Message": message}))
	h.writePage(w, status, Page{Title: "Problem — draftdeck", Body: body})
}

func (h *DashboardHandler) writePage(w http.ResponseWriter, status int, p Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(renderPage(p)))
}

func execOrEmpty(t *template.Template, data any) string {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return ""
	}
	return b.String()
}
