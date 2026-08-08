# draftdeck

Agent-published HTML draft sharing with **team review**: versioned drafts, a
byte-for-byte `/raw` contract for agents, anchored comments, and an
approve/request-changes workflow. Built after a deep study of
[postplan.dev](https://postplan.dev) — same HTML-first philosophy, plus the
team layer postplan lacks.

**Why HTML?** Coding agents already emit self-contained HTML. Serving it
byte-for-byte means a human sees the exact rendered document in a browser
while another agent (or CI) can `curl <url>/raw` and pull the artifact back
out. Markdown would only add a lossy conversion step.

## Run it

```bash
npm install
npm start                 # listens on http://localhost:4000
```

Then create your first account + API key:

```bash
npm run user:create -- --name "Maya" --email maya@team.dev
```

Point the CLI at it and save the key:

```bash
DRAFTDECK_API_URL=http://localhost:4000 node bin/draftdeck.js auth set <dd_...>
```

Enable the web dashboard (paste-a-key sign-in, drafts list, comment/approve UI):

```bash
SESSION_SECRET=<random-string> npm start
```

Environment:

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `4000` | HTTP port |
| `DATA_DIR` | `./data` | SQLite db + draft HTML files |
| `PUBLIC_BASE_URL` | `http://localhost:4000` | Base used in generated URLs |
| `SESSION_SECRET` | — | Enables dashboard sign-in + key minting |
| `MAX_HTML_BYTES` | `524288` | Upload size cap |

## The workflow

```bash
# 1. An agent writes a plan as standalone HTML, then publishes it:
node bin/draftdeck.js upload ./plan.html --description "Q3 plan"

# 2. Agent (or anyone) opens the URL; a teammate reviews it:
node bin/draftdeck.js comments <draft-id> --post "Clarify the d+12 window" --selector "#phase-2"
node bin/draftdeck.js status <draft-id> in_review

# 3. The agent pulls the feedback back (JSON), edits, re-uploads:
curl <url>/raw                          # exact bytes, for agents
node bin/draftdeck.js comments <draft-id>   # the feedback channel
node bin/draftdeck.js upload ./plan.html    # bumps to v2, resets status
node bin/draftdeck.js status <draft-id> approved
```

## API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/uploads` | optional | Upload/update HTML (`html`, `filename`, `draftId?`, `description?`, `visibility?`, `teamId?`) |
| GET | `/d/:id`, `/d/:id/raw`, `/d/:id/v/:n/raw` | team drafts only | Serve exact bytes (CSP + draft headers) |
| GET | `/api/drafts` | key | List your drafts + team drafts |
| GET | `/api/drafts/:id` | key | Detail: metadata + versions + comments |
| GET | `/api/drafts/:id/comments` | optional | Pull comments (the agent feedback channel) |
| POST | `/api/drafts/:id/comments` | key | Post anchored comment (`body`, `anchor?`, `versionNumber?`) |
| POST | `/api/drafts/:id/status` | key | `draft` / `in_review` / `changes_requested` / `approved` |
| DELETE | `/api/drafts/:id` | key | Soft-delete |
| POST | `/api/teams` | key | Create a team |
| GET | `/api/teams/:id` | key | Team + its drafts |
| POST | `/api/teams/:id/members` | key (owner) | Add member by email |

**Visibility:** `public` (listed + open), `unlisted` (random URL, open — the
default), `team` (only authenticated team members can view/edit; anonymous
requests get 401).

**HTML policy (upload-time):** static HTML + inline CSS only. Blocked: external
`<script src>`, forms, iframes/embeds/objects, inline event handlers,
`javascript:` URLs, meta-refresh. Served with a strict CSP
(`script-src 'none'` etc.) so even allowed inline scripts can't exfiltrate.

## Tests

```bash
npm test    # boots the real server, 7 end-to-end tests
```

## Layout

```
bin/draftdeck.js      CLI (upload, list, comments, status, teams, auth)
src/api.js            Express app: API + draft serving
src/web.js            Dashboard routes (paste-a-key sign-in, key minting)
src/db.js             SQLite schema + data access (node:sqlite, zero deps)
src/html-policy.js    Upload-time validation
src/storage.js        Filesystem object store (S3-shaped keys)
src/render*.js        Server-rendered pages (no build step)
scripts/create-user.js  Bootstrap accounts + keys
test/api.test.js      End-to-end tests
```
