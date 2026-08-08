export function renderHome({ publicBaseUrl }) {
  return htmlPage({
    title: "draftdeck",
    body: `
      <main class="home">
        <h1>draftdeck</h1>
        <p>Agent-published HTML drafts with team review: versioned, shareable, commentable.</p>
        <pre>npx draftdeck upload ./plan.html</pre>
        <p><a href="/dashboard">Dashboard</a> · <a href="/healthz">Health</a></p>
        <p class="muted">Public base URL: ${escapeHtml(publicBaseUrl || "not configured")}</p>
      </main>
    `
  });
}

export function renderNotFound() {
  return htmlPage({
    title: "Not found",
    body: `<main class="home"><h1>Not found</h1><p>The requested draft is unavailable.</p></main>`
  });
}

function htmlPage({ title, body }) {
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(title)}</title>
  <style>
    body { margin: 0; background: #0b0b0c; color: #f4f3ef; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; }
    .home { max-width: 760px; margin: 64px auto; padding: 0 20px; }
    h1 { margin: 0 0 12px; font-size: 40px; }
    p { color: #9a9aa3; font-size: 17px; line-height: 1.6; }
    pre { overflow-x: auto; padding: 14px; border: 1px solid #232328; background: #131316; border-radius: 6px; color: #c6f24e; }
    a { color: #c6f24e; }
    .muted { color: #6b7280; font-size: 14px; }
  </style>
</head>
<body>${body}</body>
</html>`;
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
