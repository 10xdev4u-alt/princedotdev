// Server-rendered dashboard pages. Own UI, so inline JS/styles are fine here —
// the draft-serving CSP never applies to these pages.

const STATUS_LABELS = {
  draft: "Draft",
  in_review: "In review",
  changes_requested: "Changes requested",
  approved: "Approved"
};

export function renderSignIn({ error, next }) {
  return webPage({
    title: "Sign in — draftdeck",
    body: `
      <main class="narrow center">
        <h1>draftdeck</h1>
        <p class="muted">Sign in with a draftdeck API key to see your drafts.</p>
        ${error ? `<p class="error">${escapeHtml(error)}</p>` : ""}
        <form method="post" action="/dashboard/session">
          <input type="hidden" name="next" value="${escapeHtml(next || "/dashboard")}" />
          <input class="text" type="password" name="apiKey" placeholder="dd_… paste your API key" autocomplete="off" required />
          <button class="button" type="submit">Sign in</button>
        </form>
        <p class="muted small">No key? Run <code>npm run user:create</code> on the server, or generate one from the CLI setup page.</p>
      </main>
    `
  });
}

export function renderDashboard({ session, drafts, teams }) {
  const filters = ["all", "draft", "in_review", "changes_requested", "approved"];
  const rows = drafts.length
    ? drafts
        .map(
          (d) => `
        <div class="row">
          <div>
            <a class="row-title" href="/dashboard/drafts/${escapeHtml(d.draftId)}">${escapeHtml(d.title)}</a>
            <span class="pill pill-${escapeHtml(d.status)}">${STATUS_LABELS[d.status] || d.status}</span>
            ${d.visibility === "team" ? '<span class="pill pill-team">team</span>' : ""}
            ${d.description ? `<div class="muted small">${escapeHtml(d.description)}</div>` : ""}
          </div>
          <div class="row-meta muted small">
            v${d.latestVersionNumber || "—"} · ${d.versionCount} version${d.versionCount === 1 ? "" : "s"} ·
            ${escapeHtml(formatDate(d.updatedAt))}
          </div>
        </div>`
        )
        .join("\n")
    : '<p class="muted">No drafts yet. Publish one with <code>draftdeck upload plan.html</code>.</p>';

  const teamRows = teams.length
    ? teams
        .map((t) => `<li><a href="/api/teams/${escapeHtml(t.id)}">${escapeHtml(t.name)}</a></li>`)
        .join("\n")
    : '<li class="muted">No teams yet.</li>';

  return webPage({
    title: "My drafts — draftdeck",
    header: pageHeader({ session, active: "dashboard" }),
    body: `
      <main>
        <h1>My drafts</h1>
        <div class="filters">
          ${filters
            .map(
              (f) =>
                `<button class="filter-btn ${f === "all" ? "active" : ""}" data-filter="${f}">${f === "all" ? "All" : STATUS_LABELS[f]}</button>`
            )
            .join("")}
        </div>
        <div class="list">${rows}</div>
        <h2>Teams</h2>
        <ul class="teams">${teamRows}</ul>
      </main>
      <script>
        document.querySelectorAll(".filter-btn").forEach((btn) => {
          btn.addEventListener("click", () => {
            document.querySelectorAll(".filter-btn").forEach((b) => b.classList.remove("active"));
            btn.classList.add("active");
            const status = btn.dataset.filter;
            document.querySelectorAll(".row").forEach((row) => {
              const pill = row.querySelector(".pill");
              const s = pill ? pill.textContent.trim().toLowerCase().replace(/\\s+/g, "_") : "";
              row.style.display = status === "all" || s === status ? "" : "none";
            });
          });
        });
      </script>
    `
  });
}

export function renderDraftDetail({ session, detail }) {
  const { draft, versions, comments } = detail;
  const status = STATUS_LABELS[draft.status] || draft.status;
  const versionRows = versions.length
    ? versions
        .map(
          (v) => `
        <tr>
          <td><a href="/d/${escapeHtml(draft.id)}/v/${Number(v.version_number)}" target="_blank" rel="noopener noreferrer">v${Number(v.version_number)}</a></td>
          <td>${escapeHtml(v.git_commit_subject || "")}${v.git_dirty ? ' <span class="pill pill-warn">dirty</span>' : ""}</td>
          <td class="muted mono">${escapeHtml(v.git_branch || "")} ${escapeHtml((v.git_commit_sha || "").slice(0, 7))}</td>
          <td class="muted">${escapeHtml(formatDate(v.created_at))}</td>
        </tr>`
        )
        .join("\n")
    : '<tr><td colspan="4" class="muted">No versions yet.</td></tr>';

  const commentRows = comments.length
    ? comments
        .map(
          (c) => `
        <div class="comment" id="c-${escapeHtml(c.id)}">
          <div class="comment-head">
            <strong>${escapeHtml(c.author)}</strong>
            <span class="muted small">· v${Number(c.version_number)} · ${escapeHtml(formatDate(c.created_at))}</span>
            ${c.anchor ? `<span class="muted small mono">${escapeHtml(anchorLabel(c.anchor))}</span>` : ""}
          </div>
          <div class="comment-body">${escapeHtml(c.body)}</div>
        </div>`
        )
        .join("\n")
    : '<p class="muted" id="comments">No comments yet — this is where teammates leave feedback the agent can pull back via the API.</p>';

  return webPage({
    title: `${draft.title} — draftdeck`,
    header: pageHeader({ session, active: "dashboard" }),
    body: `
      <main>
        <p class="small"><a href="/dashboard">← My drafts</a></p>
        <div class="title-row">
          <h1>${escapeHtml(draft.title)}</h1>
          <span class="pill pill-${escapeHtml(draft.status)}">${status}</span>
          ${draft.visibility === "team" ? '<span class="pill pill-team">team</span>' : ""}
        </div>
        ${draft.description ? `<p class="muted">${escapeHtml(draft.description)}</p>` : ""}
        <div class="links small">
          <a href="/d/${escapeHtml(draft.id)}" target="_blank" rel="noopener noreferrer">Open draft ↗</a> ·
          <a href="/d/${escapeHtml(draft.id)}/raw" target="_blank" rel="noopener noreferrer">Raw HTML ↗</a> ·
          <a href="/api/drafts/${escapeHtml(draft.id)}" target="_blank" rel="noopener noreferrer">JSON ↗</a>
        </div>

        <div class="status-actions">
          <form method="post" action="/dashboard/drafts/${escapeHtml(draft.id)}/status">
            <input type="hidden" name="status" value="approved" />
            <button class="button button-good" type="submit">✓ Approve</button>
          </form>
          <form method="post" action="/dashboard/drafts/${escapeHtml(draft.id)}/status">
            <input type="hidden" name="status" value="changes_requested" />
            <button class="button button-warn" type="submit">Request changes</button>
          </form>
          <form method="post" action="/dashboard/drafts/${escapeHtml(draft.id)}/status">
            <input type="hidden" name="status" value="in_review" />
            <button class="button" type="submit">Reopen review</button>
          </form>
        </div>

        <h2>Versions</h2>
        <table>
          <tr><th>Version</th><th>Commit</th><th>Ref</th><th>Published</th></tr>
          ${versionRows}
        </table>

        <h2 id="comments">Comments</h2>
        ${commentRows}
        <form class="comment-form" method="post" action="/dashboard/drafts/${escapeHtml(draft.id)}/comments">
          <textarea name="body" rows="3" placeholder="Feedback for the agent…" required></textarea>
          <div class="muted small">Tip: include a CSS selector or line reference to anchor the comment.</div>
          <button class="button" type="submit">Post comment</button>
        </form>
      </main>
    `
  });
}

export function renderCliAuth({ session, keys }) {
  const keyRows = keys.length
    ? keys
        .map(
          (k) => `
        <tr>
          <td>${escapeHtml(k.name)}</td>
          <td class="muted">${escapeHtml(formatDate(k.created_at))}</td>
          <td class="muted">${k.last_used_at ? escapeHtml(formatDate(k.last_used_at)) : "never used"}</td>
        </tr>`
        )
        .join("\n")
    : '<tr><td colspan="3" class="muted">No keys yet.</td></tr>';

  return webPage({
    title: "CLI setup — draftdeck",
    header: pageHeader({ session, active: "cli" }),
    body: `
      <main class="narrow">
        <h1>Connect your CLI</h1>
        <p class="muted">Mint a key, then run <code>draftdeck auth set &lt;key&gt;</code> on your machine (or any agent sandbox).</p>
        <form method="post" action="/cli/auth/keys">
          <button class="button" type="submit">Generate a new API key</button>
        </form>
        <h2>Active keys</h2>
        <table>
          <tr><th>Name</th><th>Created</th><th>Last used</th></tr>
          ${keyRows}
        </table>
      </main>
    `
  });
}

export function renderCliAuthKey({ session, token, keyName, keyId }) {
  return webPage({
    title: "Your new API key — draftdeck",
    header: pageHeader({ session, active: "cli" }),
    body: `
      <main class="narrow">
        <h1>Your new API key</h1>
        <p class="muted">Named <strong>${escapeHtml(keyName)}</strong>. Shown once — copy it now.</p>
        <div class="keybox">
          <code id="key">${escapeHtml(token)}</code>
          <button class="button" id="copy" type="button">Copy</button>
        </div>
        <p class="muted small">Terminal: <code>draftdeck auth set &lt;key&gt;</code> · Revoke: <code>DELETE /api/api-keys/${escapeHtml(keyId)}</code> (admin).</p>
        <script>
          document.getElementById("copy").addEventListener("click", async () => {
            await navigator.clipboard.writeText(document.getElementById("key").textContent);
            document.getElementById("copy").textContent = "Copied";
          });
        </script>
      </main>
    `
  });
}

export function renderError({ message }) {
  return webPage({
    title: "Problem — draftdeck",
    body: `
      <main class="narrow center">
        <h1>Problem</h1>
        <p class="muted">${escapeHtml(message)}</p>
        <p><a class="button" href="/dashboard">Back to dashboard</a></p>
      </main>
    `
  });
}

// ---- shared helpers ----------------------------------------------------------

function pageHeader({ session = {}, active }) {
  return `
    <header class="top">
      <nav>
        <a href="/dashboard" class="${active === "dashboard" ? "active" : ""}">My drafts</a>
        <a href="/cli/auth" class="${active === "cli" ? "active" : ""}">CLI setup</a>
      </nav>
      <form method="post" action="/auth/sign-out">
        <span class="muted small">${escapeHtml(session.accountName || "")}</span>
        <button class="linklike" type="submit">Sign out</button>
      </form>
    </header>
  `;
}

function anchorLabel(anchor) {
  try {
    const a = typeof anchor === "string" ? JSON.parse(anchor) : anchor;
    if (!a || typeof a !== "object") return "";
    if (a.selector) return `@ ${a.selector}`;
    if (Number.isFinite(a.x) && Number.isFinite(a.y)) return `@ (${a.x}, ${a.y})`;
    return a.note || "";
  } catch {
    return "";
  }
}

function formatDate(value) {
  const date = new Date(String(value).replace(" ", "T") + "Z");
  if (Number.isNaN(date.getTime())) return String(value || "");
  return date.toISOString().slice(0, 16).replace("T", " ");
}

function webPage({ title, body, header = "" }) {
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(title)}</title>
  <style>
    :root { --bg:#0b0b0c; --fg:#f4f3ef; --surface:#131316; --surface-2:#17171b; --muted:#9a9aa3; --border:#232328; --accent:#c6f24e; --good:#4ade80; --bad:#f87171; --warn:#fbbf24; }
    * { box-sizing: border-box; }
    body { margin:0; background:var(--bg); color:var(--fg); font-family:ui-sans-serif,system-ui,-apple-system,sans-serif; font-size:14px; line-height:1.6; }
    main { max-width:860px; margin:32px auto 80px; padding:0 20px; }
    main.narrow { max-width:560px; }
    main.center { text-align:center; margin-top:96px; }
    h1 { font-size:30px; margin:0 0 14px; }
    h2 { font-size:17px; margin:30px 0 8px; }
    a { color:var(--accent); }
    code { background:var(--surface-2); border:1px solid var(--border); border-radius:5px; padding:1px 5px; font-size:13px; }
    .mono { font-family:ui-monospace,"SF Mono",Menlo,monospace; }
    .muted { color:var(--muted); }
    .small { font-size:13px; }
    .error { color:var(--bad); border:1px solid var(--bad); border-radius:8px; padding:10px 14px; }
    .button { display:inline-block; background:var(--accent); color:#0b0b0c; border:0; border-radius:8px; padding:9px 16px; font-size:14px; font-weight:600; text-decoration:none; cursor:pointer; }
    .button-good { background:var(--good); }
    .button-warn { background:var(--warn); }
    .linklike { background:none; border:0; color:var(--accent); cursor:pointer; font-size:13px; padding:0; margin-left:10px; text-decoration:underline; }
    .text { background:var(--surface); border:1px solid var(--border); color:var(--fg); border-radius:8px; padding:10px 14px; font-size:14px; width:100%; margin:8px 0; outline:none; }
    .text:focus { border-color:var(--accent); }
    textarea { width:100%; background:var(--surface); border:1px solid var(--border); color:var(--fg); border-radius:8px; padding:10px 14px; font-family:inherit; font-size:14px; }
    .top { display:flex; justify-content:space-between; align-items:center; max-width:860px; margin:0 auto; padding:14px 20px; border-bottom:1px solid var(--border); }
    .top nav a { margin-right:16px; text-decoration:none; color:var(--muted); }
    .top nav a.active { color:var(--fg); font-weight:600; }
    .title-row { display:flex; align-items:center; gap:12px; }
    .title-row h1 { margin-bottom:0; }
    .links { margin:10px 0 0; }
    .list { margin-top:8px; }
    .row { display:flex; justify-content:space-between; gap:14px; align-items:baseline; padding:12px 0; border-bottom:1px solid var(--border); }
    .row-title { font-weight:600; text-decoration:none; }
    .row-meta { white-space:nowrap; }
    .filters { display:flex; gap:8px; flex-wrap:wrap; margin:8px 0 4px; }
    .filter-btn { padding:5px 12px; border-radius:999px; border:1px solid var(--border); background:var(--surface); color:var(--muted); font-size:12px; cursor:pointer; }
    .filter-btn.active { background:var(--accent); color:#0b0b0c; border-color:var(--accent); font-weight:600; }
    .pill { font-size:11px; border-radius:999px; padding:2px 8px; margin-left:6px; }
    .pill-draft { background:var(--surface-2); color:var(--muted); border:1px solid var(--border); }
    .pill-in_review { background:#1c2a1c; color:var(--accent); }
    .pill-changes_requested { background:#2a1c0c; color:var(--warn); }
    .pill-approved { background:#0c2a16; color:var(--good); }
    .pill-team { background:#1c1c2a; color:#a5b4fc; }
    .pill-warn { background:#2a1c0c; color:var(--warn); }
    .status-actions { display:flex; gap:10px; margin:18px 0; flex-wrap:wrap; }
    table { width:100%; border-collapse:collapse; margin-top:10px; }
    th, td { text-align:left; padding:8px 6px; border-bottom:1px solid var(--border); font-size:13px; }
    th { color:var(--muted); font-weight:600; font-size:11px; text-transform:uppercase; letter-spacing:0.05em; }
    .comment { background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:12px 14px; margin:8px 0; }
    .comment-head { display:flex; gap:8px; align-items:center; margin-bottom:4px; }
    .comment-body { white-space:pre-wrap; }
    .comment-form { margin-top:14px; display:flex; flex-direction:column; gap:8px; align-items:flex-start; }
    .teams { padding-left:20px; }
    .keybox { display:flex; gap:10px; align-items:center; background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:14px; margin:14px 0; }
    .keybox code { flex:1; word-break:break-all; background:none; border:0; font-size:15px; }
  </style>
</head>
<body>${header}${body}</body>
</html>`;
}

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
