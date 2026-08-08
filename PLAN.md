# draftdeck — product plan

Working title for the "better postplan" — agent-published HTML drafts with a
team review layer. Built from deep research on postplan.dev (its whole server +
CLI ship in the npm package, MIT licensed) and from the team's own usage:
RecoverKit plan docs, landing pages, and a PR Review Dashboard published as
standalone HTML.

## The insight

- **HTML is the interchange format.** Agents emit it, browsers render it, and
  `curl <url>/raw` returns it byte-for-byte — one artifact, three consumers
  (human, agent, CI). Markdown adds a lossy conversion step. This is the
  postplan bet and it's correct.
- **The storage/serving layer is a commodity.** Anyone can host HTML. The moat
  is the **workflow layer around the artifact**: private sharing, review,
  comments, approvals, provenance, teams.
- **Platform-locked answers exist but don't generalize.** Claude Code artifacts
  publish to claude.ai URLs (Anthropic-locked, interactive, not byte-fetchable
  by other agents). The gap is an agent-agnostic, protocol-level draft bus with
  a human review loop.

## MVP (this repo) — what shipped

1. **HTML contract**: versioned drafts, `/d/:id` + `/d/:id/raw` +
   `/d/:id/v/:n/raw`, exact bytes, CSP + draft headers on serve.
2. **Anonymous-by-default, keyed-when-useful**: anyone can publish; attach a
   key to own/version/delete. CLI stores per-file draft mapping so re-upload
   bumps the version of the same draft.
3. **Upload-time security policy**: static HTML + inline CSS only; blocks
   script-src, forms, iframes, event handlers, JS urls, meta-refresh. Served
   under a strict CSP.
4. **Team layer**: teams with members; `team` visibility drafts are private to
   members (401 for everyone else).
5. **Review loop**: anchored comments (selector / x,y / note), status workflow
   (draft → in_review → changes_requested/approved), agent pulls comments via
   JSON, re-upload after approval resets to draft.
6. **Dashboard**: paste-a-key sign-in, draft list with status filters, draft
   detail with versions + comments + approve/request-changes buttons, CLI key
   minting.
7. **Zero-infra stack**: Node 26 + built-in `node:sqlite` + filesystem storage.
   No native deps, no build step, runs anywhere Node runs. 7 end-to-end tests.

## The review loop (the wedge)

```
agent ──upload──▶ draftdeck ──URL──▶ teammate (browser)
  ▲                                    │
  │                                    ▼
  └──comments JSON + /raw──◀── anchored comment / approve
```

The agent is a first-class participant: it can read comments (`GET
/api/drafts/:id/comments`), fetch the artifact (`/raw`), and push revisions
that reset the workflow. That loop — human feedback consumed by a machine — is
what no existing tool does well.

## Where this goes next (by leverage)

1. **Tighten the review loop**: comment resolution state, per-version comment
   threads, "approved at v2" recording, email/notification on new versions.
2. **Team invites that actually work**: invite by email → account claim,
   membership without owner-only creation, team landing page.
3. **MCP server** so any agent publishes/reads without a CLI — the
   distribution channel for agents.
4. **S3 storage swap** (the storage layer is already S3-key-shaped) and a
   `draftdeck` npm package so teams self-host via `npx`.
5. **Previews in the dashboard** (headless screenshot per version) so humans
   scan without opening tabs.
6. **Expiry/TTLs** for public anonymous drafts.
7. **Auth upgrade**: OIDC/Google SSO for teams, replacing paste-a-key.

## Guardrails learned from postplan research

- Never serve a wrapper page: agents must always get the artifact bytes.
- Rate-limit uploads by key AND ip; hash API keys; use real client IP
  (X-Real-IP), not the spoofable left-most X-Forwarded-For.
- Express 5 pitfall: async middleware that only returns a value (never calls
  `next()`) hangs the request — always use a middleware wrapper.
- In-memory sentinel accounts must be real DB rows to satisfy foreign keys.
