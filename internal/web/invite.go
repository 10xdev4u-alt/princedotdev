package web

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

var inviteTpl = template.Must(template.New("invite").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Join {{.TeamName}} — draftdeck</title><style>
body{margin:0;background:var(--nord0,#2e3440);color:var(--nord4,#d8dee9);font-family:ui-sans-serif,system-ui,sans-serif;display:grid;place-items:center;min-height:100vh}
.card{background:#3b4252;border:1px solid #434c5e;border-radius:14px;padding:32px;max-width:420px;width:90%}
h1{margin:0 0 6px;color:#eceff4;font-size:22px}
p{margin:6px 0;color:#d8dee9}
.muted{color:#4c566a;font-size:13px}
input{width:100%;box-sizing:border-box;background:#2e3440;border:1px solid #434c5e;color:#eceff4;border-radius:8px;padding:10px 14px;font-size:14px;margin:8px 0;outline:none}
button{width:100%;background:#88c0d0;color:#2e3440;border:0;border-radius:8px;padding:11px 16px;font-size:15px;font-weight:600;cursor:pointer;margin-top:6px}
button:hover{background:#8fbcbb}
a{color:#88c0d0}
</style></head>
<body><div class="card">
  <h1>Join {{.TeamName}}</h1>
  <p>You've been invited to the <b>{{.TeamName}}</b> team on draftdeck.</p>
  <p class="muted">Invited email: <b>{{.Email}}</b></p>
  <form method="post" action="/invite/{{.Token}}">
    <input type="text" name="name" placeholder="Your name (first time here)" value="{{.NameHint}}" />
    <button type="submit">Accept invite</button>
  </form>
  <p class="muted">Accepting joins the team and signs you in. If an account already exists for this email it will be used.</p>
</div></body></html>`))

var inviteLinkTpl = template.Must(template.New("invitelink").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Invite created — draftdeck</title><style>
body{margin:0;background:#2e3440;color:#d8dee9;font-family:ui-sans-serif,system-ui,sans-serif;display:grid;place-items:center;min-height:100vh}
.card{background:#3b4252;border:1px solid #434c5e;border-radius:14px;padding:32px;max-width:520px;width:90%}
h1{margin:0 0 6px;color:#eceff4;font-size:22px}
p{margin:6px 0}
.muted{color:#4c566a;font-size:13px}
code{display:block;background:#2e3440;border:1px solid #434c5e;border-radius:8px;padding:12px 14px;margin:14px 0;word-break:break-all;color:#a3be8c;font-size:13px}
a.button{display:inline-block;background:#88c0d0;color:#2e3440;border-radius:8px;padding:10px 16px;font-weight:600;text-decoration:none}
</style></head>
<body><div class="card">
  <h1>Invite created</h1>
  <p>Sent to <b>{{.Email}}</b> for team <b>{{.TeamName}}</b>. Valid for 7 days, single use.</p>
  <p class="muted">Share this link (email, Slack, Discord — the recipient just opens it):</p>
  <code>{{.Link}}</code>
  <a class="button" href="/dashboard/settings#teams">Back to settings</a>
</div></body></html>`))

// handleInvitePage renders the magic-link accept page.
func (h *DashboardHandler) handleInvitePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.db.FindInviteByToken(token)
	if err != nil || inv == nil {
		h.writePage(w, http.StatusNotFound, Page{
			Title: "Invite — draftdeck",
			Body:  template.HTML("<main class=\"narrow center\"><h1>Invite unavailable</h1><p class=\"muted\">This invite is missing, expired, or already used.</p><p><a class=\"button\" href=\"/dashboard\">Go to dashboard</a></p></main>"),
		})
		return
	}
	team, err := h.db.FindTeam(inv.TeamID)
	if err != nil || team.ID == "" {
		h.writeError(w, http.StatusInternalServerError, "Team not found.")
		return
	}
	nameHint := localPartOf(inv.Email)
	// Prefill the name when the account already exists.
	if acct, err := h.db.FindAccountByEmail(inv.Email); err == nil && acct != nil {
		nameHint = acct.Name
	}
	h.writePage(w, http.StatusOK, Page{
		Title: "Join " + team.Name + " — draftdeck",
		Body:  template.HTML(execOrEmpty(inviteTpl, map[string]any{"TeamName": team.Name, "Email": inv.Email, "Token": token, "NameHint": nameHint})),
	})
}

// handleInviteAccept accepts the invite, signs the user in, and redirects to
// the dashboard.
func (h *DashboardHandler) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.db.FindInviteByToken(token)
	if err != nil || inv == nil {
		h.writeError(w, http.StatusNotFound, "Invite not found, expired, or already used.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	account, err := h.db.FindAccountByEmail(inv.Email)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not look up account.")
		return
	}
	if account == nil {
		if name == "" {
			name = localPartOf(inv.Email)
		}
		accountID, err := h.db.CreateAccount(name, inv.Email)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Could not create account.")
			return
		}
		account = &db.Account{ID: accountID, Name: name, Email: inv.Email}
	}
	if err := h.db.AddTeamMember(inv.TeamID, account.ID, "member"); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not join team.")
		return
	}
	_ = h.db.UseInvite(inv.ID)
	cookie, err := CreateSessionCookie(h.secret, Session{AccountID: account.ID, AccountName: account.Name, Email: account.Email})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not create session.")
		return
	}
	w.Header().Set("Set-Cookie", cookie)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// handleSettingsCreateInvite mints an invite from the team settings form and
// shows the one-time shareable link.
func (h *DashboardHandler) handleSettingsCreateInvite(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners can invite members.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if !looksLikeEmail(email) {
		h.writeError(w, http.StatusBadRequest, "A valid email is required.")
		return
	}
	inv, token, err := h.db.CreateInvite(teamID, email, session.AccountID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not create invite.")
		return
	}
	team, _ := h.db.FindTeam(teamID)
	h.writePage(w, http.StatusOK, Page{
		Title:  "Invite created — draftdeck",
		Header: h.header(session, "settings"),
		Body: template.HTML(execOrEmpty(inviteLinkTpl, map[string]any{
			"Email":    inv.Email,
			"TeamName": team.Name,
			"Link":     publicBaseURL + "/invite/" + token,
		})),
	})
}

// handleSettingsRevokeInvite deletes a pending invite.
func (h *DashboardHandler) handleSettingsRevokeInvite(w http.ResponseWriter, r *http.Request) {
	session := h.requireSession(w, r)
	if session == nil {
		return
	}
	teamID := r.PathValue("teamId")
	if ok, _ := h.db.IsTeamOwner(teamID, session.AccountID); !ok {
		h.writeError(w, http.StatusForbidden, "Only team owners can revoke invites.")
		return
	}
	if err := h.db.DeleteInvite(r.PathValue("inviteId")); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Could not revoke invite.")
		return
	}
	http.Redirect(w, r, "/dashboard/settings#teams", http.StatusFound)
}

func localPartOf(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return email
}

func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t")
}
