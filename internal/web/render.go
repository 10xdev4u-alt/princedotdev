package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// Page is the shared chrome for dashboard pages.
type Page struct {
	Title  string
	Header template.HTML
	Body   template.HTML
}

var pageTpl = template.Must(template.New("page").Parse(pageCSS + pageShell))

const pageShell = `
</head>
<body>{{.Header}}{{.Body}}</body>
</html>`

const pageCSS = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root{
    --nord0:#2e3440; --nord1:#3b4252; --nord2:#434c5e; --nord3:#4c566a;
    --nord4:#d8dee9; --nord5:#e5e9f0; --nord6:#eceff4;
    --nord7:#8fbcbb; --nord8:#88c0d0; --nord9:#81a1c1; --nord10:#5e81ac;
    --nord11:#bf616a; --nord12:#d08770; --nord13:#ebcb8b; --nord14:#a3be8c; --nord15:#b48ead;
  }
  *{box-sizing:border-box}
  body{margin:0;background:var(--nord0);color:var(--nord4);font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;font-size:14px;line-height:1.6}
  main{max-width:860px;margin:32px auto 80px;padding:0 20px}
  main.narrow{max-width:560px}
  main.center{text-align:center;margin-top:96px}
  h1{font-size:28px;margin:0 0 12px;color:var(--nord6);letter-spacing:-0.01em}
  h2{font-size:17px;margin:30px 0 8px;color:var(--nord5)}
  a{color:var(--nord8);text-decoration:none}
  a:hover{color:var(--nord7)}
  code{background:var(--nord1);border:1px solid var(--nord3);border-radius:5px;padding:1px 5px;font-size:13px;color:var(--nord8)}
  .mono{font-family:ui-monospace,"SF Mono",Menlo,monospace}
  .muted{color:var(--nord3)}
  .small{font-size:13px}
  .error{color:var(--nord11);border:1px solid var(--nord11);border-radius:8px;padding:10px 14px}
  .button{display:inline-block;background:var(--nord8);color:var(--nord0);border:0;border-radius:8px;padding:9px 16px;font-size:14px;font-weight:600;text-decoration:none;cursor:pointer}
  .button:hover{background:var(--nord7)}
  .button-good{background:var(--nord14)}
  .button-warn{background:var(--nord13);color:var(--nord0)}
  .linklike{background:none;border:0;color:var(--nord8);cursor:pointer;font-size:13px;padding:0;margin-left:10px;text-decoration:underline}
  .text{background:var(--nord1);border:1px solid var(--nord3);color:var(--nord4);border-radius:8px;padding:10px 14px;font-size:14px;width:100%;margin:8px 0;outline:none}
  .text:focus{border-color:var(--nord8)}
  textarea{width:100%;background:var(--nord1);border:1px solid var(--nord3);color:var(--nord4);border-radius:8px;padding:10px 14px;font-family:inherit;font-size:14px}
  .top{display:flex;justify-content:space-between;align-items:center;max-width:860px;margin:0 auto;padding:14px 20px;border-bottom:1px solid var(--nord2)}
  .top nav a{margin-right:16px;text-decoration:none;color:var(--nord3)}
  .top nav a.active{color:var(--nord6);font-weight:600}
  .title-row{display:flex;align-items:center;gap:12px}
  .title-row h1{margin-bottom:0}
  .links{margin:10px 0 0}
  .list{margin-top:8px}
  .row{display:flex;justify-content:space-between;gap:14px;align-items:baseline;padding:12px 0;border-bottom:1px solid var(--nord2)}
  .row-title{font-weight:600;text-decoration:none;color:var(--nord5)}
  .row-meta{white-space:nowrap}
  .filters{display:flex;gap:8px;flex-wrap:wrap;margin:8px 0 4px}
  .filter-btn{padding:5px 12px;border-radius:999px;border:1px solid var(--nord3);background:var(--nord1);color:var(--nord3);font-size:12px;cursor:pointer}
  .filter-btn.active{background:var(--nord8);color:var(--nord0);border-color:var(--nord8);font-weight:600}
  .pill{font-size:11px;border-radius:999px;padding:2px 8px;margin-left:6px}
  .pill-draft{background:var(--nord1);color:var(--nord3);border:1px solid var(--nord2)}
  .pill-in_review{background:#23364a;color:var(--nord8);border:1px solid var(--nord9)}
  .pill-changes_requested{background:#4a3a23;color:var(--nord13);border:1px solid #6b5533}
  .pill-approved{background:#2a4a33;color:var(--nord14);border:1px solid #3d6b4c}
  .pill-team{background:#352a4a;color:var(--nord15);border:1px solid #4d3d6b}
  .pill-warn{background:#4a3a23;color:var(--nord13)}
  .status-actions{display:flex;gap:10px;margin:18px 0;flex-wrap:wrap}
  table{width:100%;border-collapse:collapse;margin-top:10px}
  th,td{text-align:left;padding:8px 6px;border-bottom:1px solid var(--nord2);font-size:13px}
  th{color:var(--nord3);font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:0.05em}
  .comment{background:var(--nord1);border:1px solid var(--nord2);border-radius:8px;padding:12px 14px;margin:8px 0}
  .comment-head{display:flex;gap:8px;align-items:center;margin-bottom:4px}
  .comment-body{white-space:pre-wrap}
  .comment-form{margin-top:14px;display:flex;flex-direction:column;gap:8px;align-items:flex-start}
  .teams{padding-left:20px}
  .keybox{display:flex;gap:10px;align-items:center;background:var(--nord1);border:1px solid var(--nord2);border-radius:8px;padding:14px;margin:14px 0}
  .keybox code{flex:1;word-break:break-all;background:none;border:0;font-size:15px}
  .meter{height:12px;background:var(--nord1);border:1px solid var(--nord2);border-radius:999px;overflow:hidden;margin:6px 0 2px}
  .meter-fill{height:100%;background:var(--nord8);border-radius:999px}
  .meter-fill.warn{background:var(--nord13)}
  .meter-fill.bad{background:var(--nord11)}
  .inline-form{display:flex;gap:8px;align-items:center;margin-top:8px}
  .text.inline{width:260px;margin:0}
  .bad{color:var(--nord11)}
</style>`

func renderPage(p Page) string {
	var b strings.Builder
	_ = pageTpl.Execute(&b, p)
	return b.String()
}

// ---- page fragments -----------------------------------------------------------

var signInTpl = template.Must(template.New("signin").Parse(`
<main class="narrow center">
  <h1>draftdeck</h1>
  <p class="muted">Sign in with a draftdeck API key to see your drafts.</p>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form method="post" action="/dashboard/session">
    <input class="text" type="password" name="apiKey" placeholder="dd_… paste your API key" autocomplete="off" required />
    <button class="button" type="submit">Sign in</button>
  </form>
  <p class="muted small">No key? Generate one with the CLI setup page after creating an account.</p>
</main>`))

var headerTpl = template.Must(template.New("header").Parse(`
<header class="top">
  <nav>
    <a href="/dashboard" {{if eq .Active "dashboard"}}class="active"{{end}}>My drafts</a>
    <a href="/cli/auth" {{if eq .Active "cli"}}class="active"{{end}}>CLI setup</a>
    <a href="/dashboard/settings" {{if eq .Active "settings"}}class="active"{{end}}>Settings</a>
  </nav>
  <form method="post" action="/auth/sign-out">
    <span class="muted small">{{.AccountName}}</span>
    <button class="linklike" type="submit">Sign out</button>
  </form>
</header>`))

var dashboardTpl = template.Must(template.New("dashboard").Parse(`
<main>
  <h1>My drafts</h1>
  <div class="filters">
    <button class="filter-btn active" data-filter="all">All</button>
    <button class="filter-btn" data-filter="draft">Draft</button>
    <button class="filter-btn" data-filter="in_review">In review</button>
    <button class="filter-btn" data-filter="changes_requested">Changes requested</button>
    <button class="filter-btn" data-filter="approved">Approved</button>
  </div>
  <div class="list">
    {{if .Drafts}}
      {{range .Drafts}}
      <div class="row">
        <div>
          <a class="row-title" href="/dashboard/drafts/{{.DraftID}}">{{.Title}}</a>
          <span class="pill pill-{{.Status}}">{{.StatusLabel}}</span>
          {{if eq .Visibility "team"}}<span class="pill pill-team">team</span>{{end}}
          {{if .Description}}<div class="muted small">{{.Description}}</div>{{end}}
        </div>
        <div class="row-meta muted small">v{{.LatestLabel}} · {{.VersionCount}} version{{if ne .VersionCount 1}}s{{end}} · {{.UpdatedLabel}}</div>
      </div>
      {{end}}
    {{else}}
      <p class="muted">No drafts yet. Publish one with <code>draftdeck upload plan.html</code>.</p>
    {{end}}
  </div>
  <h2>Teams</h2>
  <ul class="teams">
    {{if .Teams}}
      {{range .Teams}}<li><a href="/api/teams/{{.ID}}">{{.Name}}</a></li>{{end}}
    {{else}}
      <li class="muted">No teams yet.</li>
    {{end}}
  </ul>
</main>
<script>
  document.querySelectorAll(".filter-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".filter-btn").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      const status = btn.dataset.filter;
      document.querySelectorAll(".row").forEach((row) => {
        const pill = row.querySelector(".pill");
        const s = pill ? pill.textContent.trim().toLowerCase().replace(/\s+/g, "_") : "";
        row.style.display = status === "all" || s === status ? "" : "none";
      });
    });
  });
</script>`))

var draftDetailTpl = template.Must(template.New("detail").Parse(`
<main>
  <p class="small"><a href="/dashboard">← My drafts</a></p>
  <div class="title-row">
    <h1>{{.Title}}</h1>
    <span class="pill pill-{{.Status}}">{{.StatusLabel}}</span>
    {{if eq .Visibility "team"}}<span class="pill pill-team">team</span>{{end}}
  </div>
  {{if .Description}}<p class="muted">{{.Description}}</p>{{end}}
  <div class="links small">
    <a href="/d/{{.DraftID}}" target="_blank" rel="noopener noreferrer">Open draft ↗</a> ·
    <a href="/d/{{.DraftID}}/raw" target="_blank" rel="noopener noreferrer">Raw HTML ↗</a> ·
    <a href="/api/drafts/{{.DraftID}}" target="_blank" rel="noopener noreferrer">JSON ↗</a>
  </div>

  <div class="status-actions">
    <form method="post" action="/dashboard/drafts/{{.DraftID}}/status">
      <input type="hidden" name="status" value="approved" />
      <button class="button button-good" type="submit">✓ Approve</button>
    </form>
    <form method="post" action="/dashboard/drafts/{{.DraftID}}/status">
      <input type="hidden" name="status" value="changes_requested" />
      <button class="button button-warn" type="submit">Request changes</button>
    </form>
    <form method="post" action="/dashboard/drafts/{{.DraftID}}/status">
      <input type="hidden" name="status" value="in_review" />
      <button class="button" type="submit">Reopen review</button>
    </form>
  </div>

  <h2>Versions</h2>
  <table>
    <tr><th>Version</th><th>Commit</th><th>Ref</th><th>Published</th></tr>
    {{if .Versions}}
      {{range .Versions}}
      <tr>
        <td><a href="/d/{{$.DraftID}}/v/{{.VersionNumber}}" target="_blank" rel="noopener noreferrer">v{{.VersionNumber}}</a></td>
        <td>{{.CommitSubject}}{{if .GitDirty}} <span class="pill pill-warn">dirty</span>{{end}}</td>
        <td class="muted mono">{{.Branch}} {{.ShortSHA}}</td>
        <td class="muted">{{if .Author}}{{.Author}} · {{end}}{{.CreatedLabel}}</td>
      </tr>
      {{end}}
    {{else}}
      <tr><td colspan="4" class="muted">No versions yet.</td></tr>
    {{end}}
  </table>

  <h2 id="comments">Comments</h2>
  {{if .Comments}}
    {{range .Comments}}
    <div class="comment" id="c-{{.ID}}">
      <div class="comment-head">
        <strong>{{.Author}}</strong>
        <span class="muted small">· v{{.VersionNumber}} · {{.CreatedLabel}}</span>
        {{if .AnchorLabel}}<span class="muted small mono">{{.AnchorLabel}}</span>{{end}}
      </div>
      <div class="comment-body">{{.Body}}</div>
    </div>
    {{end}}
  {{else}}
    <p class="muted">No comments yet — this is where teammates leave feedback the agent can pull back via the API.</p>
  {{end}}
  <form class="comment-form" method="post" action="/dashboard/drafts/{{.DraftID}}/comments">
    <textarea name="body" rows="3" placeholder="Feedback for the agent…" required></textarea>
    <div class="muted small">Tip: include a CSS selector or line reference to anchor the comment.</div>
    <button class="button" type="submit">Post comment</button>
  </form>
</main>`))

var cliAuthTpl = template.Must(template.New("cli").Parse(`
<main class="narrow">
  <h1>Connect your CLI</h1>
  <p class="muted">Mint a key, then run <code>draftdeck auth set &lt;key&gt;</code> on your machine (or any agent sandbox).</p>
  <form method="post" action="/cli/auth/keys">
    <button class="button" type="submit">Generate a new API key</button>
  </form>
  <h2>Active keys</h2>
  <table>
    <tr><th>Name</th><th>Created</th><th>Last used</th></tr>
    {{if .Keys}}
      {{range .Keys}}
      <tr>
        <td>{{.Name}}</td>
        <td class="muted">{{.CreatedLabel}}</td>
        <td class="muted">{{.LastUsedLabel}}</td>
      </tr>
      {{end}}
    {{else}}
      <tr><td colspan="3" class="muted">No keys yet.</td></tr>
    {{end}}
  </table>
</main>`))

var cliAuthKeyTpl = template.Must(template.New("cli-key").Parse(`
<main class="narrow">
  <h1>Your new API key</h1>
  <p class="muted">Named <strong>{{.KeyName}}</strong>. Shown once — copy it now.</p>
  <div class="keybox">
    <code id="key">{{.Token}}</code>
    <button class="button" id="copy" type="button">Copy</button>
  </div>
  <p class="muted small">Terminal: <code>draftdeck auth set &lt;key&gt;</code></p>
  <script>
    document.getElementById("copy").addEventListener("click", async () => {
      await navigator.clipboard.writeText(document.getElementById("key").textContent);
      document.getElementById("copy").textContent = "Copied";
    });
  </script>
</main>`))

var settingsTpl = template.Must(template.New("settings").Parse(`
<main>
  <h1>Settings</h1>

  <h2 id="storage">Storage</h2>
  <div class="meter">
    <div class="meter-fill {{.MeterClass}}" style="width: {{.UsedPercent}}%"></div>
  </div>
  <p class="muted small">{{.UsedLabel}} of {{.BudgetLabel}} used · {{.DraftCount}} drafts · {{.VersionCount}} versions · {{.CommentCount}} comments · {{.AccountCount}} accounts · {{.TeamCount}} teams</p>

  <h2 id="api-keys">API keys</h2>
  <table>
    <tr><th>Name</th><th>Created</th><th>Last used</th><th></th></tr>
    {{if .Keys}}
      {{range .Keys}}
      <tr>
        <td>{{.Name}}</td>
        <td class="muted">{{.CreatedLabel}}</td>
        <td class="muted">{{.LastUsedLabel}}</td>
        <td>
          <form method="post" action="/dashboard/settings/keys/{{.ID}}/revoke" onsubmit="return confirm('Revoke this key?')">
            <button class="linklike bad" type="submit">Revoke</button>
          </form>
        </td>
      </tr>
      {{end}}
    {{else}}
      <tr><td colspan="4" class="muted">No keys yet — mint one on the <a href="/cli/auth">CLI setup</a> page.</td></tr>
    {{end}}
  </table>

  {{range .Teams}}
  <h2>Team: {{.Name}}</h2>
  <table>
    <tr><th>Member</th><th>Email</th><th>Role</th><th></th></tr>
    {{range .Members}}
    <tr>
      <td>{{.Name}}</td>
      <td class="muted">{{.Email}}</td>
      <td>{{.Role}}</td>
      <td>
        {{if eq .Role "owner"}}<span class="muted small">owner</span>{{else}}
        <form method="post" action="/dashboard/settings/teams/{{$.TeamID}}/members/{{.AccountID}}/remove" onsubmit="return confirm('Remove {{.Name}}?')">
          <button class="linklike bad" type="submit">Remove</button>
        </form>
        {{end}}
      </td>
    </tr>
    {{end}}
  </table>
  <form class="inline-form" method="post" action="/dashboard/settings/teams/{{.TeamID}}/members/add">
    <input class="text inline" type="email" name="email" placeholder="teammate@team.dev" required />
    <button class="button" type="submit">Add member</button>
  </form>
  {{end}}
</main>`))

var errorTpl = template.Must(template.New("error").Parse(`
<main class="narrow center">
  <h1>Problem</h1>
  <p class="muted">{{.Message}}</p>
  <p><a class="button" href="/dashboard">Back to dashboard</a></p>
</main>`))

// ---- data shaping --------------------------------------------------------------

var statusLabels = map[string]string{
	"draft":             "Draft",
	"in_review":         "In review",
	"changes_requested": "Changes requested",
	"approved":          "Approved",
}

func statusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

// draftRow is one dashboard list row.
type draftRow struct {
	DraftID      string
	Title        string
	Description  string
	Visibility   string
	Status       string
	StatusLabel  string
	LatestLabel  string
	VersionCount int64
	UpdatedLabel string
}

// versionRow is one version table row.
type versionRow struct {
	VersionNumber int64
	CommitSubject string
	Branch        string
	ShortSHA      string
	GitDirty      bool
	Author        string
	CreatedLabel  string
}

// commentRow is one comment card.
type commentRow struct {
	ID            string
	Author        string
	VersionNumber int64
	Body          string
	AnchorLabel   string
	CreatedLabel  string
}

func formatDate(value string) string {
	if value == "" {
		return ""
	}
	// SQLite datetime('now') yields "YYYY-MM-DD HH:MM:SS" (UTC).
	t, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		return value
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// formatBytes renders byte counts human-readably (storage meter).
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	switch {
	case exp == 0:
		return fmt.Sprintf("%.1f KB", val)
	case exp == 1:
		return fmt.Sprintf("%.1f MB", val)
	case exp == 2:
		return fmt.Sprintf("%.1f GB", val)
	default:
		return fmt.Sprintf("%.1f TB", val)
	}
}

func anchorLabel(raw string) string {
	if raw == "" {
		return ""
	}
	var a map[string]any
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return ""
	}
	if sel, ok := a["selector"].(string); ok && sel != "" {
		return "@ " + sel
	}
	if x, ok := a["x"].(float64); ok {
		if y, ok2 := a["y"].(float64); ok2 {
			return "@ (" + itoa(int64(x)) + ", " + itoa(int64(y)) + ")"
		}
	}
	if note, ok := a["note"].(string); ok && note != "" {
		return note
	}
	return ""
}
