package server

import (
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// handleCreateInvite mints a magic-link invite for a team (owners only). The
// link is returned once; whoever holds it can join the team as the invited
// email — share it like an org invite.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	teamID := r.PathValue("teamId")
	team, err := s.db.FindTeam(teamID)
	if err != nil || team.ID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Team not found."})
		return
	}
	if ok, _ := s.db.IsTeamOwner(teamID, key.AccountID); !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only team owners can invite members."})
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(email) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "A valid email is required."})
		return
	}
	inv, token, err := s.db.CreateInvite(teamID, email, key.AccountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":        true,
		"invite":    inv,
		"inviteUrl": s.cfg.PublicBaseURL + "/invite/" + token,
	})
}

// handleGetInvite resolves a magic link (no auth — the token is the credential).
func (s *Server) handleGetInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := s.db.FindInviteByToken(r.PathValue("token"))
	if err != nil || inv == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Invite not found, expired, or already used."})
		return
	}
	team, err := s.db.FindTeam(inv.TeamID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"teamId":    team.ID,
		"teamName":  team.Name,
		"email":     inv.Email,
		"expiresAt": inv.ExpiresAt,
	})
}

// handleAcceptInvite joins the invited email to the team. If no account exists
// for that email one is created (name defaults to the email local-part). A
// fresh API key is minted so CLI/agent workflows can accept invites too.
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := s.db.FindInviteByToken(r.PathValue("token"))
	if err != nil || inv == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Invite not found, expired, or already used."})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = decodeJSON(r, &req)

	account, err := s.db.FindAccountByEmail(inv.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if account == nil {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = localPart(inv.Email)
		}
		accountID, err := s.db.CreateAccount(name, inv.Email)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		account = &db.Account{ID: accountID, Name: name, Email: inv.Email}
	}
	if err := s.db.AddTeamMember(inv.TeamID, account.ID, "member"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	if err := s.db.UseInvite(inv.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	s.recordTeamActivity("member_joined", account.Name, "joined via invite", inv.TeamID)
	_, apiToken, err := s.db.CreateAPIKey(account.ID, "invite")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	team, _ := s.db.FindTeam(inv.TeamID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"accountId":   account.ID,
		"accountName": account.Name,
		"email":       account.Email,
		"teamId":      inv.TeamID,
		"teamName":    team.Name,
		"apiKey":      apiToken,
	})
}

func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t")
}

func localPart(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return email
}
