package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// Webhook events. A webhook's Events column holds a comma-separated subset.
const (
	evUpload  = "upload"
	evComment = "comment"
	evStatus  = "status"
)

var validWebhookKinds = map[string]bool{"discord": true, "slack": true, "generic": true}
var validWebhookEvents = map[string]bool{evUpload: true, evComment: true, evStatus: true}

// webhookEvent is the notification payload for a single trigger.
type webhookEvent struct {
	Event         string
	Actor         string
	Draft         *db.Draft
	VersionNumber int64
	Comment       *db.Comment
	FromStatus    string
	ToStatus      string
}

// fireWebhooks delivers ev to every webhook subscribed to its event type and
// scoped to the draft's owner or team. Delivery is fire-and-forget: each
// endpoint gets a goroutine with bounded retries; results are recorded on the
// webhook row so the settings page shows delivery health.
func (s *Server) fireWebhooks(ev webhookEvent) {
	if ev.Draft == nil {
		return
	}
	all, err := s.db.ListWebhooks()
	if err != nil {
		return
	}
	for _, h := range all {
		if !wantsEvent(h, ev.Event) {
			continue
		}
		// Scope: personal webhooks fire for the owner's drafts; team
		// webhooks fire for that team's drafts.
		if h.TeamID != "" {
			if ev.Draft.TeamID != h.TeamID {
				continue
			}
		} else if ev.Draft.AccountID != h.AccountID {
			continue
		}
		w := h
		go s.deliverWebhook(w, buildWebhookPayload(w.Kind, ev))
	}
}

func wantsEvent(h db.Webhook, ev string) bool {
	for _, e := range strings.Split(h.Events, ",") {
		if strings.TrimSpace(e) == ev {
			return true
		}
	}
	return false
}

// deliverWebhook POSTs the payload with bounded retries (3 attempts, 500ms
// base backoff) and records the outcome.
func (s *Server) deliverWebhook(h db.Webhook, payload []byte) {
	status := 0
	var errMsg string
	for attempt := 1; attempt <= 3; attempt++ {
		status, errMsg = postJSON(h.URL, payload)
		if status >= 200 && status < 300 {
			errMsg = ""
			break
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	_ = s.db.SetWebhookResult(h.ID, status, errMsg)
}

func postJSON(target string, payload []byte) (int, string) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Post(target, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	return resp.StatusCode, ""
}

// buildWebhookPayload renders the event in the channel's wire format.
func buildWebhookPayload(kind string, ev webhookEvent) []byte {
	base := map[string]any{
		"event":      ev.Event,
		"draft":      draftPayload(ev.Draft),
		"actor":      orAnonymous(ev.Actor),
		"version":    ev.VersionNumber,
		"fromStatus": ev.FromStatus,
		"toStatus":   ev.ToStatus,
		"sentAt":     time.Now().UTC().Format(time.RFC3339),
	}
	if ev.Comment != nil {
		base["comment"] = map[string]any{
			"body":          ev.Comment.Body,
			"anchor":        ev.Comment.Anchor,
			"versionNumber": ev.Comment.VersionNumber,
		}
	}
	switch kind {
	case "discord":
		return mustJSON(discordPayload(ev))
	case "slack":
		return mustJSON(slackPayload(ev))
	default:
		return mustJSON(base)
	}
}

func draftPayload(d *db.Draft) map[string]any {
	return map[string]any{
		"id":         d.ID,
		"title":      d.Title,
		"status":     d.Status,
		"visibility": d.Visibility,
		"teamId":     orEmpty(d.TeamID),
	}
}

func discordPayload(ev webhookEvent) map[string]any {
	title := draftTitle(ev)
	color := statusColor(ev.ToStatus)
	desc := eventLine(ev)
	fields := []map[string]any{}
	if ev.ToStatus != "" {
		fields = append(fields, map[string]any{"name": "Status", "value": ev.ToStatus, "inline": true})
	}
	if ev.VersionNumber > 0 {
		fields = append(fields, map[string]any{"name": "Version", "value": "v" + strconv.FormatInt(ev.VersionNumber, 10), "inline": true})
	}
	if ev.Draft.TeamID != "" {
		fields = append(fields, map[string]any{"name": "Team", "value": ev.Draft.TeamID, "inline": true})
	}
	embed := map[string]any{
		"title":       title,
		"description": desc,
		"color":       color,
		"fields":      fields,
		"footer":      map[string]any{"text": "draftdeck · " + orAnonymous(ev.Actor)},
	}
	return map[string]any{"event": ev.Event, "content": nil, "embeds": []map[string]any{embed}}
}

func slackPayload(ev webhookEvent) map[string]any {
	text := "*" + draftTitle(ev) + "*\n" + eventLine(ev)
	return map[string]any{
		"event": ev.Event,
		"text":  text,
		"blocks": []map[string]any{
			{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
			{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": "draftdeck · " + orAnonymous(ev.Actor)}}},
		},
	}
}

func draftTitle(ev webhookEvent) string {
	if ev.Draft.Title == "" {
		return "Untitled Draft"
	}
	return ev.Draft.Title
}

func eventLine(ev webhookEvent) string {
	switch ev.Event {
	case evUpload:
		return "published a new version"
	case evComment:
		body := ""
		if ev.Comment != nil {
			body = ": " + truncate(ev.Comment.Body, 200)
		}
		return "commented" + body
	case evStatus:
		if ev.FromStatus != "" && ev.ToStatus != "" {
			return "moved " + ev.FromStatus + " → " + ev.ToStatus
		}
		return "changed status to " + ev.ToStatus
	}
	return ev.Event
}

func statusColor(status string) int {
	switch status {
	case "approved":
		return 0x4ade80
	case "in_review":
		return 0xfbbf24
	case "changes_requested":
		return 0xf87171
	}
	return 0xc6f24e // nordic accent
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func orEmpty(s string) string { return s }
func orAnonymous(s string) string {
	if s == "" {
		return "Anonymous"
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- management API ----------------------------------------------------------

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	var req struct {
		Name   string   `json:"name"`
		Kind   string   `json:"kind"`
		URL    string   `json:"url"`
		Events []string `json:"events"`
		TeamID string   `json:"teamId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid JSON body."})
		return
	}
	name := cleanText(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name is required."})
		return
	}
	if req.Kind == "" {
		req.Kind = "discord"
	}
	if !validWebhookKinds[req.Kind] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "kind must be one of: discord, slack, generic."})
		return
	}
	if err := validateWebhookURL(req.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	events := ""
	if len(req.Events) == 0 {
		events = evUpload + "," + evComment + "," + evStatus
	} else {
		var parts []string
		for _, e := range req.Events {
			if !validWebhookEvents[e] {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "event \"" + e + "\" is invalid."})
				return
			}
			parts = append(parts, e)
		}
		events = strings.Join(parts, ",")
	}
	if req.TeamID != "" {
		if ok, _ := s.db.IsTeamOwner(req.TeamID, key.AccountID); !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Only team owners can create team webhooks."})
			return
		}
	}
	wh, err := s.db.CreateWebhook(db.Webhook{
		AccountID: key.AccountID,
		TeamID:    req.TeamID,
		Name:      name,
		Kind:      req.Kind,
		URL:       strings.TrimSpace(req.URL),
		Events:    events,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "webhook": wh})
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	key := authFrom(r)
	all, err := s.db.ListWebhooks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	out := make([]db.Webhook, 0, len(all))
	for _, h := range all {
		if s.canManageWebhook(h, key) {
			out = append(out, h)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhooks": out})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
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
	if err := s.db.DeleteWebhook(wh.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
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
	ev := webhookEvent{
		Event: "test",
		Actor: key.AccountName,
		Draft: &db.Draft{ID: "test", Title: "Test event", Status: "draft", Visibility: "unlisted"},
	}
	status, errMsg := postJSON(wh.URL, buildWebhookPayload(wh.Kind, ev))
	_ = s.db.SetWebhookResult(wh.ID, status, errMsg)
	if status >= 200 && status < 300 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "status": status, "error": errMsg})
}

func (s *Server) canManageWebhook(h db.Webhook, key *db.APIKey) bool {
	if key == nil {
		return false
	}
	if h.AccountID == key.AccountID {
		return true
	}
	if h.TeamID != "" {
		if ok, _ := s.db.IsTeamOwner(h.TeamID, key.AccountID); ok {
			return true
		}
	}
	return false
}

func validateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url is required.")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url is invalid.")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url must be http(s).")
	}
	if u.Host == "" {
		return fmt.Errorf("url must include a host.")
	}
	return nil
}

func decodeJSON(r *http.Request, into any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
}
