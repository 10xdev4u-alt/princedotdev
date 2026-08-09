// Package digest builds the daily review summary and its notification
// payloads. Shared by the server (scheduled + API pushes) and the web
// dashboard (manual "send digest" trigger).
package digest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// Draft is one draft line in a digest.
type Draft struct {
	Title         string `json:"title"`
	Status        string `json:"status"`
	VersionNumber int64  `json:"versionNumber"`
	Approved      int64  `json:"approved"`
	Required      int64  `json:"required"`
}

// Summary is the assembled digest for a team.
type Summary struct {
	TeamName string  `json:"teamName"`
	Summary  string  `json:"summary"`
	Drafts   []Draft `json:"drafts"`
}

// Build assembles the review-needs-attention summary for a team.
func Build(d *db.DB, team db.Team) *Summary {
	drafts, err := d.ListTeamDrafts(team.ID)
	if err != nil {
		return nil
	}
	sum := &Summary{TeamName: team.Name}
	open := 0
	cleared := 0
	for _, it := range drafts {
		if it.Status != "in_review" && it.Status != "changes_requested" {
			continue
		}
		open++
		dd := Draft{
			Title:         it.Title,
			Status:        it.Status,
			VersionNumber: it.LatestVersionNumber,
		}
		if team.RequiredApprovals > 0 {
			var count int64
			if hasRev, _ := d.DraftHasReviewers(it.DraftID); hasRev {
				count, _ = d.ReviewerApprovalCount(it.DraftID)
			} else {
				count, _ = d.ApprovalCount(it.DraftID)
			}
			dd.Approved = count
			dd.Required = team.RequiredApprovals
			if count >= team.RequiredApprovals {
				cleared++
			}
		}
		sum.Drafts = append(sum.Drafts, dd)
	}
	switch {
	case open == 0:
		sum.Summary = "All caught up — nothing in review."
	case open == 1:
		sum.Summary = "1 draft needs review."
	default:
		sum.Summary = strconv.Itoa(open) + " drafts need review."
	}
	if cleared > 0 {
		sum.Summary += " " + strconv.Itoa(cleared) + " cleared the gate."
	}
	return sum
}

// DiscordPayload renders the daily summary as one embed with a field per
// draft still in review.
func DiscordPayload(sum *Summary) map[string]any {
	fields := make([]map[string]any, 0, len(sum.Drafts))
	for _, d := range sum.Drafts {
		value := "v" + strconv.FormatInt(d.VersionNumber, 10)
		if d.Required > 0 {
			value += " · " + strconv.FormatInt(d.Approved, 10) + "/" + strconv.FormatInt(d.Required, 10) + " approved"
		}
		value += " · " + StatusHuman(d.Status)
		fields = append(fields, map[string]any{"name": truncate(d.Title, 80), "value": value, "inline": true})
	}
	return map[string]any{
		"event": "digest",
		"embeds": []map[string]any{{
			"title":       "📬 Daily digest — " + sum.TeamName,
			"description": sum.Summary,
			"color":       0x81a1c1,
			"fields":      fields,
			"footer":      map[string]any{"text": "draftdeck"},
		}},
	}
}

// SlackPayload renders the daily summary as Slack blocks.
func SlackPayload(sum *Summary) map[string]any {
	var b strings.Builder
	fmt.Fprintf(&b, "*📬 Daily digest — %s*\n%s", sum.TeamName, sum.Summary)
	for _, d := range sum.Drafts {
		line := "• " + d.Title + " (v" + strconv.FormatInt(d.VersionNumber, 10) + ")"
		if d.Required > 0 {
			line += " — " + strconv.FormatInt(d.Approved, 10) + "/" + strconv.FormatInt(d.Required, 10) + " approved"
		}
		b.WriteString("\n" + line)
	}
	return map[string]any{
		"event": "digest",
		"text":  b.String(),
		"blocks": []map[string]any{
			{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": b.String()}},
			{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": "draftdeck"}}},
		},
	}
}

// Payload renders the digest for a webhook kind.
func Payload(kind string, sum *Summary) []byte {
	switch kind {
	case "discord":
		return mustJSON(DiscordPayload(sum))
	case "slack":
		return mustJSON(SlackPayload(sum))
	default:
		return mustJSON(map[string]any{"event": "digest", "digest": sum})
	}
}

func StatusHuman(s string) string {
	switch s {
	case "draft":
		return "Draft"
	case "in_review":
		return "In review"
	case "changes_requested":
		return "Changes requested"
	case "approved":
		return "Approved"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
