package web

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/diff"
	"github.com/10xdev4u-alt/princedotdev/internal/store"
)

// NewDashboard builds the web UI handler. Sessions are disabled when secret
// is empty (no SESSION_SECRET). storageBudget feeds the settings meter; st
// backs the version diff page.
func NewDashboard(cfgSecret string, d *db.DB, storageBudget int64, st *store.Store) *DashboardHandler {
	return &DashboardHandler{db: d, secret: cfgSecret, budget: storageBudget, store: st}
}

// DashboardHandler serves the web UI. Safe for concurrent ServeHTTP calls.
type DashboardHandler struct {
	db     *db.DB
	secret string
	budget int64
	store  *store.Store
}

// Enabled reports whether sessions are configured (SESSION_SECRET set).
func (h *DashboardHandler) Enabled() bool { return h.secret != "" }

// Routes registers the dashboard routes on mux.
func (h *DashboardHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard", h.handleDashboard)
	mux.HandleFunc("POST /dashboard/session", h.handleSession)
	mux.HandleFunc("POST /auth/sign-out", h.handleSignOut)
	mux.HandleFunc("GET /dashboard/drafts/{draftId}", h.handleDraftPage)
	mux.HandleFunc("GET /dashboard/drafts/{draftId}/diff", h.handleDraftDiffPage)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/comments", h.handlePostComment)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/status", h.handlePostStatus)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/tags", h.handleDraftTags)
	mux.HandleFunc("POST /dashboard/drafts/{draftId}/reviewers", h.handleDraftReviewers)
	mux.HandleFunc("GET /cli/auth", h.handleCLIAuth)
	mux.HandleFunc("POST /cli/auth/keys", h.handleMintKey)
	mux.HandleFunc("GET /dashboard/settings", h.handleSettings)
	mux.HandleFunc("POST /dashboard/settings/keys/{keyId}/revoke", h.handleRevokeKey)
	mux.HandleFunc("POST /dashboard/settings/teams/{teamId}/members/add", h.handleSettingsAddMember)
	mux.HandleFunc("POST /dashboard/settings/teams/{teamId}/members/{accountId}/remove", h.handleSettingsRemoveMember)
	mux.HandleFunc("POST /dashboard/settings/webhooks/add", h.handleSettingsAddWebhook)
	mux.HandleFunc("POST /dashboard/settings/webhooks/{webhookId}/test", h.handleSettingsTestWebhook)
	mux.HandleFunc("POST /dashboard/settings/webhooks/{webhookId}/delete", h.handleSettingsDeleteWebhook)
	mux.HandleFunc("GET /invite/{token}", h.handleInvitePage)
	mux.HandleFunc("POST /invite/{token}", h.handleInviteAccept)
	mux.HandleFunc("GET /dashboard/control", h.handleControlPage)
	mux.HandleFunc("GET /dashboard/teams/{teamId}", h.handleTeamPage)
	mux.HandleFunc("POST /dashboard/teams/{teamId}/settings", h.handleTeamSettings)
	mux.HandleFunc("POST /dashboard/teams/{teamId}/members/{accountId}/role", h.handleTeamSetRole)
	mux.HandleFunc("POST /dashboard/activity/read", h.handleMarkActivityRead)
	mux.HandleFunc("POST /dashboard/settings/teams/{teamId}/invites/add", h.handleSettingsCreateInvite)
	mux.HandleFunc("POST /dashboard/settings/teams/{teamId}/invites/{inviteId}/revoke", h.handleSettingsRevokeInvite)
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
	// Filters: ?q=, &status=, &tag=, &teamId=
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	fStatus := r.URL.Query().Get("status")
	fTag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	fTeam := r.URL.Query().Get("teamId")
	ids := make([]string, 0, len(items))
	filtered := items[:0]
	for _, it := range items {
		if fStatus != "" && it.Status != fStatus {
			continue
		}
		if fTeam != "" && it.TeamID != fTeam {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title), q) && !strings.Contains(strings.ToLower(it.Description), q) {
			continue
		}
		ids = append(ids, it.DraftID)
		filtered = append(filtered, it)
	}
	tagsByDraft, err := h.db.TagsForDrafts(ids)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load tags.")
		return
	}
	if fTag != "" {
		kept := filtered[:0]
		for _, it := range filtered {
			if hasTag(tagsByDraft[it.DraftID], fTag) {
				kept = append(kept, it)
			}
		}
		filtered = kept
	}
	allTags, err := h.db.AllTagsForAccount(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load tags.")
		return
	}
	rows := make([]draftRow, 0, len(filtered))
	for _, it := range filtered {
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
			Tags:         tagsByDraft[it.DraftID],
		})
	}
	activity, err := h.db.ListActivity(session.AccountID, 20)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load activity.")
		return
	}
	unread, err := h.db.UnreadActivity(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not count activity.")
		return
	}
	type actRow struct {
		Kind      string
		KindLabel string
		Actor     string
		Body      string
		DraftID   string
		TimeLabel string
	}
	aRows := make([]actRow, 0, len(activity))
	for _, a := range activity {
		aRows = append(aRows, actRow{
			Kind:      a.Kind,
			KindLabel: activityLabel(a.Kind),
			Actor:     a.Actor,
			Body:      a.Body,
			DraftID:   a.DraftID,
			TimeLabel: formatDate(a.CreatedAt),
		})
	}
	body := template.HTML(execOrEmpty(dashboardTpl, map[string]any{
		"Drafts":   rows,
		"Teams":    teams,
		"Activity": aRows,
		"Unread":   unread,
		"Query":    r.URL.Query().Get("q"),
		"FStatus":  fStatus,
		"FTag":     fTag,
		"FTeam":    fTeam,
		"AllTags":  allTags,
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
	tags, err := h.db.DraftTags(draft.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load tags.")
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
			Author:        v.Author,
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
	reviewers, _ := h.db.ListDraftReviewers(draft.ID)
	apprStatus, _ := h.db.ReviewerApprovalStatus(draft.ID)
	rRows := make([]reviewerRow, 0, len(reviewers))
	assigned := map[string]bool{}
	for _, rv := range reviewers {
		assigned[rv.AccountID] = true
		rRows = append(rRows, reviewerRow{
			Name:     rv.Name,
			Email:    rv.Email,
			Approved: apprStatus[rv.AccountID],
		})
	}
	canAssign := h.canAssignReviewers(draft, session)
	var memberRows []memberCheck
	if draft.TeamID != "" {
		if members, err := h.db.ListTeamMembers(draft.TeamID); err == nil {
			for _, m := range members {
				memberRows = append(memberRows, memberCheck{
					AccountID: m.AccountID,
					Name:      m.Name,
					Checked:   assigned[m.AccountID],
				})
			}
		}
	}
	body := template.HTML(execOrEmpty(draftDetailTpl, map[string]any{
		"DraftID":     draft.ID,
		"Title":       draft.Title,
		"Description": draft.Description,
		"Visibility":  draft.Visibility,
		"Tags":        tags,
		"Status":      draft.Status,
		"StatusLabel": statusLabel(draft.Status),
		"Versions":    vRows,
		"Comments":    cRows,
		"Reviewers":   rRows,
		"CanAssign":   canAssign,
		"TeamMembers": memberRows,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  draft.Title + " — draftdeck",
		Header: h.header(session, "dashboard"),
		Body:   body,
	})
}

// canAssignReviewers reports whether the session may change reviewers:
// the draft owner, or a team owner/admin for team drafts.
func (h *DashboardHandler) canAssignReviewers(draft *db.Draft, session *Session) bool {
	if session == nil {
		return false
	}
	if draft.AccountID == session.AccountID {
		return true
	}
	if draft.TeamID == "" {
		return false
	}
	ok, _ := h.db.IsTeamAdmin(draft.TeamID, session.AccountID)
	return ok
}

func (h *DashboardHandler) handleDraftReviewers(w http.ResponseWriter, r *http.Request) {
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
	if !h.canAssignReviewers(draft, session) {
		h.writeError(w, http.StatusForbidden, "Only the draft owner or a team owner/admin can assign reviewers.")
		return
	}
	var ids []string
	for _, id := range r.Form["reviewers"] {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if draft.TeamID != "" {
			if ok, _ := h.db.IsTeamMember(draft.TeamID, id); !ok {
				h.writeError(w, http.StatusBadRequest, "Reviewers must be team members.")
				return
			}
		}
		ids = append(ids, id)
	}
	if err := h.db.SetDraftReviewers(draft.ID, ids, session.AccountID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not save reviewers.")
		return
	}
	http.Redirect(w, r, "/dashboard/drafts/"+draft.ID+"#reviewers", http.StatusFound)
}

// diffLine is one rendered line of the diff page.
type diffLine struct {
	Kind   string
	Prefix string
	Text   string
	OldN   int
	NewN   int
}

// diffHunk is one hunk with a header and lines.
type diffHunk struct {
	Header string
	Lines  []diffLine
}

// diffVersion is a picker entry for one draft version.
type diffVersion struct {
	Number int64
	Label  string
}

func (h *DashboardHandler) handleDraftDiffPage(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || len(versions) < 2 {
		h.writeError(w, http.StatusNotFound, "Not enough versions to diff.")
		return
	}

	from := int64(0)
	if v := r.URL.Query().Get("from"); v != "" {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	to := int64(0)
	if v := r.URL.Query().Get("to"); v != "" {
		to, _ = strconv.ParseInt(v, 10, 64)
	}
	// Default: compare the newest version against the one before it.
	if to == 0 {
		to = versions[0].VersionNumber
	}
	if from == 0 {
		for _, v := range versions {
			if v.VersionNumber < to {
				from = v.VersionNumber
				break
			}
		}
	}
	if from == 0 || from == to {
		h.writeError(w, http.StatusBadRequest, "Choose two different versions to compare.")
		return
	}

	vFrom, err := h.db.GetVersion(draft.ID, from)
	if err != nil || vFrom == nil {
		h.writeError(w, http.StatusNotFound, "Version not found.")
		return
	}
	vTo, err := h.db.GetVersion(draft.ID, to)
	if err != nil || vTo == nil {
		h.writeError(w, http.StatusNotFound, "Version not found.")
		return
	}
	oldHTML, err := h.store.Get(vFrom.ObjectKey)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not read version content.")
		return
	}
	newHTML, err := h.store.Get(vTo.ObjectKey)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not read version content.")
		return
	}

	hunks := diff.Lines(string(oldHTML), string(newHTML))
	rows := make([]diffHunk, 0, len(hunks))
	for _, hk := range hunks {
		lines := make([]diffLine, 0, len(hk.Lines))
		for _, l := range hk.Lines {
			prefix := " "
			switch l.Kind {
			case diff.KindAdd:
				prefix = "+"
			case diff.KindDel:
				prefix = "−"
			}
			lines = append(lines, diffLine{Kind: l.Kind, Prefix: prefix, Text: l.Text, OldN: l.OldN, NewN: l.NewN})
		}
		rows = append(rows, diffHunk{
			Header: hunkHeader(hk.OldStart, hk.OldCount, hk.NewStart, hk.NewCount),
			Lines:  lines,
		})
	}
	added, removed := diff.Counts(hunks)

	picks := make([]diffVersion, 0, len(versions))
	for _, v := range versions {
		picks = append(picks, diffVersion{Number: v.VersionNumber, Label: "v" + itoa(v.VersionNumber)})
	}

	body := template.HTML(execOrEmpty(dashboardDiffTpl, map[string]any{
		"DraftID":   draft.ID,
		"Title":     draft.Title,
		"From":      from,
		"To":        to,
		"Versions":  picks,
		"Hunks":     rows,
		"Added":     added,
		"Removed":   removed,
		"FromLabel": formatDate(vFrom.CreatedAt),
		"ToLabel":   formatDate(vTo.CreatedAt),
		"HasDiff":   len(rows) > 0,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "Diff · " + draft.Title + " — draftdeck",
		Header: h.header(session, "dashboard"),
		Body:   body,
	})
}

func hunkHeader(oldStart, oldCount, newStart, newCount int) string {
	return "@@ -" + itoa(int64(oldStart)) + "," + itoa(int64(oldCount)) + " +" + itoa(int64(newStart)) + "," + itoa(int64(newCount)) + " @@"
}

// controlTeamRow is one team's storage line on the control page.
type controlTeamRow struct {
	TeamID      string
	TeamName    string
	DraftCount  int64
	StoredLabel string
	Percent     int
}

// auditRow is one audit-log line on the control page.
type auditRow struct {
	TimeLabel string
	Actor     string
	Action    string
	Target    string
	Details   string
}

func (h *DashboardHandler) handleControlPage(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	stats, err := h.db.Stats()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load stats.")
		return
	}
	teams, err := h.db.ListTeamsForAccount(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load teams.")
		return
	}
	allUsage, err := h.db.TeamStorageUsage()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load team storage.")
		return
	}
	tRows := make([]controlTeamRow, 0, len(allUsage))
	for _, ts := range allUsage {
		if ok, _ := h.db.IsTeamAdmin(ts.TeamID, session.AccountID); !ok {
			continue
		}
		pct := 0
		if h.budget > 0 {
			pct = int(float64(ts.StoredBytes) / float64(h.budget) * 100)
			if pct > 100 {
				pct = 100
			}
		}
		tRows = append(tRows, controlTeamRow{
			TeamID:      ts.TeamID,
			TeamName:    ts.TeamName,
			DraftCount:  ts.DraftCount,
			StoredLabel: formatBytes(ts.StoredBytes),
			Percent:     pct,
		})
	}
	// Audit trail: the caller's own actions plus every team they admin.
	entries := []db.AuditEntry{}
	if own, err := h.db.ListAudit("", session.AccountID, 50); err == nil {
		entries = append(entries, own...)
	}
	for _, t := range teams {
		if ok, _ := h.db.IsTeamAdmin(t.ID, session.AccountID); ok {
			if teamEntries, err := h.db.ListAudit(t.ID, "", 100); err == nil {
				entries = append(entries, teamEntries...)
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].CreatedAt > entries[j].CreatedAt })
	if len(entries) > 100 {
		entries = entries[:100]
	}
	aRows := make([]auditRow, 0, len(entries))
	for _, e := range entries {
		aRows = append(aRows, auditRow{
			TimeLabel: formatDate(e.CreatedAt),
			Actor:     e.Actor,
			Action:    e.Action,
			Target:    e.Target,
			Details:   e.Details,
		})
	}
	usedPercent := float64(0)
	meterClass := ""
	if h.budget > 0 {
		usedPercent = float64(stats.StoredBytes) / float64(h.budget) * 100
		switch {
		case usedPercent >= 90:
			meterClass = "bad"
		case usedPercent >= 70:
			meterClass = "warn"
		}
		if usedPercent > 100 {
			usedPercent = 100
		}
	}
	body := template.HTML(execOrEmpty(controlTpl, map[string]any{
		"UsedPercent":  int(usedPercent),
		"MeterClass":   meterClass,
		"UsedLabel":    formatBytes(stats.StoredBytes),
		"BudgetLabel":  formatBytes(h.budget),
		"DraftCount":   stats.DraftCount,
		"VersionCount": stats.VersionCount,
		"Teams":        tRows,
		"Entries":      aRows,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "Control — draftdeck",
		Header: h.header(session, "control"),
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

// ---- settings (control panel) ------------------------------------------------

// handleDraftTags saves tags from the draft detail page.
func (h *DashboardHandler) handleDraftTags(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	draftID := r.PathValue("draftId")
	draft, err := h.db.FindDraft(draftID)
	if err != nil || draft == nil || draft.DeletedAt != "" {
		h.writeError(w, http.StatusNotFound, "Draft not found.")
		return
	}
	if !h.canView(draft, session) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this draft.")
		return
	}
	raw := r.FormValue("tags")
	var tags []string
	for _, part := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(part); t != "" {
			tags = append(tags, t)
		}
	}
	if err := h.db.SetDraftTags(draftID, tags); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not save tags.")
		return
	}
	http.Redirect(w, r, "/dashboard/drafts/"+draftID, http.StatusFound)
}

func (h *DashboardHandler) handleMarkActivityRead(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	if err := h.db.MarkActivityRead(session.AccountID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not mark activity read.")
		return
	}
	http.Redirect(w, r, "/dashboard#activity", http.StatusFound)
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func activityLabel(kind string) string {
	switch kind {
	case "upload":
		return "Upload"
	case "comment":
		return "Comment"
	case "status":
		return "Status"
	case "mention":
		return "Mention"
	case "member_joined":
		return "Team"
	}
	return kind
}

func (h *DashboardHandler) handleSettings(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	stats, err := h.db.Stats()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load stats.")
		return
	}
	keys, err := h.db.ListAPIKeys(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load keys.")
		return
	}
	teams, err := h.db.ListTeamsForAccount(session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load teams.")
		return
	}
	type memberRow struct {
		AccountID string
		Name      string
		Email     string
		Role      string
	}
	type inviteRow struct {
		ID      string
		Email   string
		Created string
		Used    bool
	}
	type teamRow struct {
		TeamID  string
		Name    string
		Members []memberRow
		Invites []inviteRow
	}
	tRows := make([]teamRow, 0, len(teams))
	for _, t := range teams {
		members, err := h.db.ListTeamMembers(t.ID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Could not load team members.")
			return
		}
		mRows := make([]memberRow, 0, len(members))
		for _, m := range members {
			mRows = append(mRows, memberRow{AccountID: m.AccountID, Name: m.Name, Email: m.Email, Role: m.Role})
		}
		invites, err := h.db.ListInvites(t.ID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Could not load invites.")
			return
		}
		iRows := make([]inviteRow, 0, len(invites))
		for _, inv := range invites {
			iRows = append(iRows, inviteRow{ID: inv.ID, Email: inv.Email, Created: formatDate(inv.CreatedAt), Used: inv.UsedAt != ""})
		}
		tRows = append(tRows, teamRow{TeamID: t.ID, Name: t.Name, Members: mRows, Invites: iRows})
	}
	type keyRow struct {
		ID            string
		Name          string
		CreatedLabel  string
		LastUsedLabel string
	}
	kRows := make([]keyRow, 0, len(keys))
	for _, k := range keys {
		last := "never used"
		if k.LastUsedAt != "" {
			last = formatDate(k.LastUsedAt)
		}
		kRows = append(kRows, keyRow{ID: k.ID, Name: k.Name, CreatedLabel: formatDate(k.CreatedAt), LastUsedLabel: last})
	}
	webhooks, err := h.db.ListWebhooks()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load webhooks.")
		return
	}
	type webhookDeliveryRow struct {
		TimeLabel   string
		Event       string
		Status      string
		StatusLabel string
		OK          bool
	}
	type webhookRow struct {
		ID          string
		Name        string
		Kind        string
		EventsLabel string
		LastLabel   string
		TeamName    string
		Deliveries  []webhookDeliveryRow
	}
	wRows := make([]webhookRow, 0, len(webhooks))
	teamName := map[string]string{}
	for _, t := range teams {
		teamName[t.ID] = t.Name
	}
	for _, wh := range webhooks {
		if !h.canManageWebhook(wh, session.AccountID) {
			continue
		}
		events := strings.ReplaceAll(wh.Events, ",", ", ")
		last := "never sent"
		if wh.LastStatus >= 200 && wh.LastStatus < 300 {
			last = strconv.FormatInt(wh.LastStatus, 10) + " OK"
		} else if wh.LastStatus > 0 || wh.LastError != "" {
			last = "failed"
			if wh.LastError != "" {
				last = truncateStr(wh.LastError, 60)
			}
		}
		deliveries, _ := h.db.ListWebhookDeliveries(wh.ID, 5)
		dRows := make([]webhookDeliveryRow, 0, len(deliveries))
		for _, dl := range deliveries {
			dRows = append(dRows, webhookDeliveryRow{
				TimeLabel:   formatDate(dl.CreatedAt),
				Event:       dl.Event,
				Status:      strconv.FormatInt(int64(dl.Status), 10),
				StatusLabel: "delivered",
				OK:          dl.Status >= 200 && dl.Status < 300,
			})
		}
		wRows = append(wRows, webhookRow{
			ID:          wh.ID,
			Name:        wh.Name,
			Kind:        wh.Kind,
			EventsLabel: events,
			LastLabel:   last,
			TeamName:    teamName[wh.TeamID],
			Deliveries:  dRows,
		})
	}
	usedPercent := float64(0)
	meterClass := ""
	if h.budget > 0 {
		usedPercent = float64(stats.StoredBytes) / float64(h.budget) * 100
		switch {
		case usedPercent >= 90:
			meterClass = "bad"
		case usedPercent >= 70:
			meterClass = "warn"
		}
		if usedPercent > 100 {
			usedPercent = 100
		}
	}
	body := template.HTML(execOrEmpty(settingsTpl, map[string]any{
		"UsedPercent":  int(usedPercent),
		"MeterClass":   meterClass,
		"UsedLabel":    formatBytes(stats.StoredBytes),
		"BudgetLabel":  formatBytes(h.budget),
		"DraftCount":   stats.DraftCount,
		"VersionCount": stats.VersionCount,
		"CommentCount": stats.CommentCount,
		"AccountCount": stats.AccountCount,
		"TeamCount":    stats.TeamCount,
		"Keys":         kRows,
		"Teams":        tRows,
		"Webhooks":     wRows,
	}))
	h.writePage(w, http.StatusOK, Page{
		Title:  "Settings — draftdeck",
		Header: h.header(session, "settings"),
		Body:   body,
	})
}

func (h *DashboardHandler) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	if err := h.db.RevokeAPIKey(r.PathValue("keyId"), session.AccountID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not revoke key.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings#api-keys", http.StatusFound)
}

func (h *DashboardHandler) handleSettingsAddMember(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners can add members.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if email == "" {
		http.Redirect(w, r, "/dashboard/settings?error=empty-email", http.StatusFound)
		return
	}
	account, err := h.db.FindAccountByEmail(email)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not look up account.")
		return
	}
	if account == nil {
		http.Redirect(w, r, "/dashboard/settings?error=no-account", http.StatusFound)
		return
	}
	if err := h.db.AddTeamMember(teamID, account.ID, "member"); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not add member.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings", http.StatusFound)
}

// ---- webhooks (settings) -----------------------------------------------------

func (h *DashboardHandler) handleSettingsAddWebhook(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	kind := r.FormValue("kind")
	url := strings.TrimSpace(r.FormValue("url"))
	if kind == "" {
		kind = "discord"
	}
	if name == "" || url == "" {
		http.Redirect(w, r, "/dashboard/settings?error=webhook-missing", http.StatusFound)
		return
	}
	if !validWebhookKind(kind) || !validWebhookURL(url) {
		http.Redirect(w, r, "/dashboard/settings?error=webhook-invalid", http.StatusFound)
		return
	}
	events := r.Form["events"]
	if len(events) == 0 {
		events = []string{"upload", "comment", "status"}
	}
	teamID := strings.TrimSpace(r.FormValue("teamId"))
	if teamID != "" {
		if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
			h.writeError(w, http.StatusForbidden, "Only team owners can create team webhooks.")
			return
		}
	}
	_, err := h.db.CreateWebhook(db.Webhook{
		AccountID: session.AccountID,
		TeamID:    teamID,
		Name:      name,
		Kind:      kind,
		URL:       url,
		Events:    strings.Join(events, ","),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not create webhook.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings#webhooks", http.StatusFound)
}

func (h *DashboardHandler) handleSettingsTestWebhook(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	wh, err := h.db.FindWebhook(r.PathValue("webhookId"))
	if err != nil || wh == nil {
		http.NotFound(w, r)
		return
	}
	if !h.canManageWebhook(*wh, session.AccountID) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this webhook.")
		return
	}
	status, errMsg := postTestWebhook(wh.URL, wh.Kind)
	_ = h.db.SetWebhookResult(wh.ID, status, errMsg)
	if status >= 200 && status < 300 {
		http.Redirect(w, r, "/dashboard/settings#webhooks", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?error=webhook-failed#webhooks", http.StatusFound)
}

func (h *DashboardHandler) handleSettingsDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	wh, err := h.db.FindWebhook(r.PathValue("webhookId"))
	if err != nil || wh == nil {
		http.NotFound(w, r)
		return
	}
	if !h.canManageWebhook(*wh, session.AccountID) {
		h.writeError(w, http.StatusForbidden, "You don't have access to this webhook.")
		return
	}
	if err := h.db.DeleteWebhook(wh.ID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not delete webhook.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings#webhooks", http.StatusFound)
}

func (h *DashboardHandler) canManageWebhook(wh db.Webhook, accountID string) bool {
	if wh.AccountID == accountID {
		return true
	}
	if wh.TeamID != "" {
		if ok, _ := h.db.IsTeamOwner(wh.TeamID, accountID); ok {
			return true
		}
	}
	return false
}

func validWebhookKind(kind string) bool {
	return kind == "discord" || kind == "slack" || kind == "generic"
}

func validWebhookURL(raw string) bool {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return false
	}
	return strings.Contains(raw, "/")
}

// postTestWebhook sends a minimal channel-appropriate ping and returns the
// HTTP status plus any error string.
func postTestWebhook(url, kind string) (int, string) {
	var payload []byte
	switch kind {
	case "slack":
		payload = []byte(`{"text":"draftdeck webhook test — if you see this, the channel is wired 🎉"}`)
	case "generic":
		payload = []byte(`{"event":"test","message":"draftdeck webhook test"}`)
	default:
		payload = []byte(`{"content":"draftdeck webhook test — if you see this, the channel is wired 🎉"}`)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	return resp.StatusCode, ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (h *DashboardHandler) handleSettingsRemoveMember(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners can remove members.")
		return
	}
	if err := h.db.RemoveTeamMember(teamID, r.PathValue("accountId")); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not remove member.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings", http.StatusFound)
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
