package web

import "fmt"

// HomeHTML renders the landing page in the Nord palette (arcticicestudio's
// nord): deep polar-night surfaces, frost-blue accents, snow-storm text.
func HomeHTML(publicBaseURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>draftdeck</title>
<style>
  :root{
    --nord0:#2e3440; --nord1:#3b4252; --nord2:#434c5e; --nord3:#4c566a;
    --nord4:#d8dee9; --nord5:#e5e9f0; --nord6:#eceff4;
    --nord7:#8fbcbb; --nord8:#88c0d0; --nord9:#81a1c1; --nord10:#5e81ac;
    --nord11:#bf616a; --nord12:#d08770; --nord13:#ebcb8b; --nord14:#a3be8c; --nord15:#b48ead;
  }
  *{box-sizing:border-box}
  body{margin:0;background:var(--nord0);color:var(--nord4);
       font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;line-height:1.6}
  main{max-width:760px;margin:72px auto;padding:0 24px}
  h1{margin:0 0 10px;font-size:40px;letter-spacing:-0.02em;color:var(--nord6)}
  h1 span{color:var(--nord8)}
  p{color:var(--nord3);font-size:17px}
  pre{overflow-x:auto;padding:16px 18px;border:1px solid var(--nord3);border-radius:10px;
      background:var(--nord1);color:var(--nord8);font-size:14px}
  a{color:var(--nord8);text-decoration:none}
  a:hover{color:var(--nord7)}
  .links{margin-top:22px;display:flex;gap:18px}
  .muted{color:var(--nord3);font-size:13px}
</style>
</head>
<body>
<main>
  <h1>draftdeck<span>.</span></h1>
  <p>Agent-published HTML drafts with team review — versioned, shareable, commentable.</p>
  <pre>npx draftdeck upload ./plan.html</pre>
  <div class="links">
    <a href="/dashboard">Dashboard</a>
    <a href="/healthz">Health</a>
  </div>
  <p class="muted">Public base URL: %s</p>
</main>
</body>
</html>`, publicBaseURL)
}
