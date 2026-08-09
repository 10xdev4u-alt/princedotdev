package server

import (
	"net/http"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/digest"
)

const (
	digestInterval = 30 * time.Minute
	digestPeriod   = 24 * time.Hour
)

// pushDigestToTeam builds and delivers the digest to every digest-subscribed
// webhook for the team, records it in the feed, and stamps last_digest_at.
func (s *Server) pushDigestToTeam(team db.Team) {
	sum := digest.Build(s.db, team)
	if sum == nil {
		return
	}
	all, err := s.db.ListWebhooks()
	if err != nil {
		return
	}
	sent := false
	for _, h := range all {
		if h.TeamID != team.ID || !wantsEvent(h, evDigest) {
			continue
		}
		sent = true
		go s.deliverWebhook(h, evDigest, digest.Payload(h.Kind, sum))
	}
	if sent {
		s.recordTeamActivity("digest", "draftdeck", sum.Summary, team.ID)
		_ = s.db.SetTeamLastDigest(team.ID)
	}
}

// digestLoop is the background scheduler: every digestInterval it pushes the
// digest to each team that has a digest webhook and hasn't been sent in
// digestPeriod. Started in New, stopped by Close.
func (s *Server) digestLoop() {
	t := time.NewTicker(digestInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			teams, err := s.db.ListTeams()
			if err != nil {
				continue
			}
			now := time.Now().UTC()
			for _, team := range teams {
				last, _ := s.db.GetTeamLastDigest(team.ID)
				if last != "" {
					if lt, err := time.Parse("2006-01-02 15:04:05", last); err == nil {
						if now.Sub(lt) < digestPeriod {
							continue
						}
					}
				}
				s.pushDigestToTeam(team)
			}
		case <-s.digestStop:
			return
		}
	}
}

// handleSendDigest manually triggers a digest push for the webhook's team.
func (s *Server) handleSendDigest(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	wh, err := s.db.FindWebhook(r.PathValue("webhookId"))
	if err != nil || wh == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Webhook not found."})
		return
	}
	if !s.canManageWebhook(*wh, key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "You don't have access to this webhook."})
		return
	}
	if wh.TeamID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Digests need a team webhook."})
		return
	}
	team, err := s.db.FindTeam(wh.TeamID)
	if err != nil || team.ID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Team not found."})
		return
	}
	s.pushDigestToTeam(team)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
