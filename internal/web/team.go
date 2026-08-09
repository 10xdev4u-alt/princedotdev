package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

var teamTpl = template.Must(template.New("team").Parse(`
<main>
  <h1>{{.Name}} <span class="pill pill-team">team</span></h1>
  <p class="muted small">Created {{.CreatedAt}} · {{len .Members}} members{{if gt .RequiredApprovals 0}} · approval gate: {{.RequiredApprovals}} reviewers{{end}}</p>

  <h2>Approval gate</h2>
  {{if .IsAdmin}}
  <form class="inline-form" method="post" action="/dashboard/teams/{{.TeamID}}/settings">
    <label class="small">Required approvals to mark a draft approved:</label>
    <input class="text inline" type="number" name="requiredApprovals" min="0" max="50" value="{{.RequiredApprovals}}" style="width:80px" />
    <button class="button" type="submit">Save</button>
  </form>
  {{else}}
  <p class="muted small">{{.RequiredApprovals}} approval{{if ne .RequiredApprovals 1}}s{{end}} required before drafts are marked approved. Only owners/admins can change this.</p>
  {{end}}

  <h2>Members</h2>
  <table>
    <tr><th>Member</th><th>Email</th><th>Role</th><th></th></tr>
    {{range .Members}}
    <tr>
      <td>{{.Name}}</td>
      <td class="muted">{{.Email}}</td>
      <td>{{.Role}}</td>
      <td>
        {{if and $.IsOwner (ne .Role "owner")}}
        <form class="inline-form" method="post" action="/dashboard/teams/{{$.TeamID}}/members/{{.AccountID}}/role">
          <select class="text inline" name="role" style="width:auto">
            <option value="member" {{if eq .Role "member"}}selected{{end}}>member</option>
            <option value="admin" {{if eq .Role "admin"}}selected{{end}}>admin</option>
          </select>
          <button class="linklike" type="submit">Set</button>
        </form>
        {{else if and $.IsOwner (eq .Role "owner")}}<span class="muted small">owner</span>{{end}}
      </td>
    </tr>
    {{end}}
  </table>

  <h2>Drafts</h2>
  <div class="list">
    {{if .Drafts}}
      {{range .Drafts}}
      <div class="row">
        <div>
          <a class="row-title" href="/dashboard/drafts/{{.DraftID}}">{{.Title}}</a>
          <span class="pill pill-{{.Status}}">{{.StatusLabel}}</span>
          {{if .Approvals}}<span class="muted small">{{.Approvals}}</span>{{end}}
        </div>
        <div class="row-meta muted small">v{{.LatestLabel}} · {{.UpdatedLabel}}</div>
      </div>
      {{end}}
    {{else}}
      <p class="muted">No team drafts yet. Publish one with <code>draftdeck upload plan.html --team {{.TeamID}}</code>.</p>
    {{end}}
  </div>
</main>`))

// handleTeamPage renders the per-team dashboard.
func (h *DashboardHandler) handleTeamPage(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	team, err := h.db.FindTeam(teamID)
	if err != nil || team.ID == "" {
		h.writeError(w, http.StatusNotFound, "Team not found.")
		return
	}
	member, err := h.db.IsTeamMember(teamID, session.AccountID)
	if err != nil || !member {
		h.writeError(w, http.StatusForbidden, "You're not a member of this team.")
		return
	}
	isOwner, _ := h.db.IsTeamOwner(teamID, session.AccountID)
	isAdmin, _ := h.db.IsTeamAdmin(teamID, session.AccountID)

	members, err := h.db.ListTeamMembers(teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load members.")
		return
	}
	type memberRow struct {
		AccountID string
		Name      string
		Email     string
		Role      string
	}
	mRows := make([]memberRow, 0, len(members))
	for _, m := range members {
		mRows = append(mRows, memberRow{AccountID: m.AccountID, Name: m.Name, Email: m.Email, Role: m.Role})
	}

	drafts, err := h.db.ListTeamDrafts(teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not load drafts.")
		return
	}
	type draftRowT struct {
		DraftID      string
		Title        string
		Status       string
		StatusLabel  string
		Approvals    string
		LatestLabel  string
		UpdatedLabel string
	}
	dRows := make([]draftRowT, 0, len(drafts))
	for _, d := range drafts {
		approvals := ""
		if team.RequiredApprovals > 0 {
			count, _ := h.db.ApprovalCount(d.DraftID)
			approvals = strconv.FormatInt(count, 10) + "/" + strconv.FormatInt(team.RequiredApprovals, 10) + " approvals"
		}
		latest := "—"
		if d.LatestVersionNumber > 0 {
			latest = itoa(d.LatestVersionNumber)
		}
		dRows = append(dRows, draftRowT{
			DraftID:      d.DraftID,
			Title:        d.Title,
			Status:       d.Status,
			StatusLabel:  statusLabel(d.Status),
			Approvals:    approvals,
			LatestLabel:  latest,
			UpdatedLabel: formatDate(d.UpdatedAt),
		})
	}
	h.writePage(w, http.StatusOK, Page{
		Title:  team.Name + " — draftdeck",
		Header: h.header(session, "teams"),
		Body: template.HTML(execOrEmpty(teamTpl, map[string]any{
			"TeamID":            team.ID,
			"Name":              team.Name,
			"CreatedAt":         formatDate(team.CreatedAt),
			"RequiredApprovals": team.RequiredApprovals,
			"Members":           mRows,
			"Drafts":            dRows,
			"IsOwner":           isOwner,
			"IsAdmin":           isAdmin,
			"ApprovalsRequired": team.RequiredApprovals,
		})),
	})
}

// handleTeamSettings saves the approval gate (owners/admins).
func (h *DashboardHandler) handleTeamSettings(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamAdmin(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners and admins can update team settings.")
		return
	}
	n, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("requiredApprovals")), 10, 64)
	if err != nil || n < 0 || n > 50 {
		h.writeError(w, http.StatusBadRequest, "requiredApprovals must be a number between 0 and 50.")
		return
	}
	if err := h.db.UpdateTeamApprovals(teamID, n); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not update team.")
		return
	}
	http.Redirect(w, r, "/dashboard/teams/"+teamID, http.StatusFound)
}

// handleTeamSetRole changes a member's role (owners only).
func (h *DashboardHandler) handleTeamSetRole(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners can change roles.")
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role != "admin" && role != "member" {
		h.writeError(w, http.StatusBadRequest, "role must be admin or member.")
		return
	}
	if err := h.db.SetMemberRole(teamID, r.PathValue("accountId"), role); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not change role.")
		return
	}
	http.Redirect(w, r, "/dashboard/teams/"+teamID, http.StatusFound)
}
