# draftdeck

Agent-published HTML drafts with team review. An agent writes a plan as a
standalone HTML file, publishes it, and the team reviews it — comments,
approvals, and revisions, all anchored to exact versions.

- **HTML is the contract** — drafts are stored and served byte-for-byte. Browsers render them; agents `curl` the `/raw` URL and pull structured data back.
- **Versioned** — every re-upload of the same draft becomes `v2, v3…` with full git provenance (branch, SHA, subject, dirty state).
- **Team review** — private team drafts, anchored comments, and a `draft → in_review → changes_requested → approved` workflow that the agent can drive via the API.
- **Single static Go binary** — server, CLI, and backup tool in one ~15 MB binary. SQLite embedded (WAL mode, no CGO). Runs anywhere Docker runs.

## Run it (Docker, recommended)

```bash
docker run -d --name draftdeck -p 8080:8080 \
  -e SESSION_SECRET=$(openssl rand -hex 32) \
  -v draftdeck-data:/data \
  ghcr.io/10xdev4u-alt/princedotdev:latest
```

Or with compose (healthcheck + named volume included):

```bash
docker compose up -d
```

The image is published to **ghcr.io/10xdev4u-alt/princedotdev** (`:latest`,
plus `:v0.1.0` etc.) by the CI workflow on every push / tag.

### Run it from source

```bash
go run ./cmd/draftdeck                 # server on :8080
go build -o draftdeck ./cmd/draftdeck  # single binary
draftdeck serve                        # default command
```

Environment:

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `DATA_DIR` | `./data` | SQLite db + draft HTML files (volume `/data` in the container) |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Base used in generated URLs |
| `SESSION_SECRET` | — | Enables dashboard sign-in + key minting |
| `STORAGE_BUDGET_BYTES` | `5368709120` (5 GiB) | Total stored HTML cap; over-budget uploads get 507 |
| `MAX_HTML_BYTES` | `524288` | Per-upload size cap |
| `UPLOAD_RATE_LIMIT_MAX` | `30` | Uploads per key/IP per window |

## The workflow

```bash
# 1. An agent writes a plan as standalone HTML, then publishes it:
npx @princetheprogrammerbtw/draftdeck upload ./plan.html --description "Q3 plan"
#    Uploaded draft
#    URL: http://localhost:8080/d/abc123def456
#    Raw HTML: http://localhost:8080/d/abc123def456/raw

# 2. A teammate reviews it — comments, approve, or request changes:
npx @princetheprogrammerbtw/draftdeck comments abc123def456 --post "Add a migration section" --selector "h1"
npx @princetheprogrammerbtw/draftdeck status abc123def456 approved

# 3. The agent pulls the feedback back as JSON, edits, re-uploads (→ v2):
curl http://localhost:8080/api/drafts/abc123def456/comments
npx @princetheprogrammerbtw/draftdeck upload ./plan.html
```

## CLI

The npx client (`npx @princetheprogrammerbtw/draftdeck …`) downloads the native binary from GitHub
Releases on first run and caches it — no install step. `draftdeck` commands:

| Command | Purpose |
|---|---|
| `auth set <key>` / `auth whoami` | Save / verify credentials |
| `upload <file>` | Publish or update (re-upload versions the same draft) |
| `list` | Drafts with status, versions, time-ago (`--json` for agents) |
| `comments <id>` | Read feedback, or `--post "…" --selector "h1"` to leave it |
| `status <id> <status>` | `draft \| in_review \| changes_requested \| approved` |
| `teams` / `teams create --name X` / `teams members --team T --email E` | Shared workspaces |

State lives in `~/.draftdeck/`; env overrides: `DRAFTDECK_API_URL`,
`DRAFTDECK_API_KEY`, `DRAFTDECK_HOME`, `DRAFTDECK_CACHE`, `DRAFTDECK_BIN_BASE`.

## MCP server (agents)

`draftdeck mcp` speaks the Model Context Protocol over stdio, so Claude Code /
Codebuff can publish drafts and drive review natively — no curl:

```bash
# against the running container:
claude mcp add draftdeck -- docker exec -i dd-local draftdeck mcp
# or a local binary:
DRAFTDECK_API_URL=http://localhost:8080 DRAFTDECK_API_KEY=dd_… draftdeck mcp
```

Tools: `upload_draft`, `list_drafts`, `get_draft`, `list_comments`,
`post_comment`, `set_status`, `list_teams`. The upload validates HTML with the
same policy the server enforces.

## Web dashboard

`SESSION_SECRET` enables `/dashboard`: paste-an-API-key sign-in (sessions are
HMAC-signed cookies), the drafts list with status filters, draft detail with
version history + comment thread + Approve/Request-changes buttons, and the
CLI setup page that mints keys. Nord-palette, server-rendered, no JS build.

## API

`POST /api/uploads` (anonymous or `Authorization: Bearer dd_…`), `GET /d/{id}`
and `/d/{id}/raw` (byte-for-byte with a hard CSP + draft headers),
`/api/drafts` (list/detail/comments/status), `/api/teams` (+members),
`/api/me`. All JSON responses are `{ ok, … }`; errors carry `{ ok: false, error }`.

## Data & backups (consistency)

See the full model in `PLAN.md`. In short: SQLite in WAL mode with a 5 s busy
timeout; the container keeps data on a named volume so image updates never
touch it; uploads are crash-tolerant (file → version row → pointer, orphans at
worst); and consistent online snapshots are one command:

```bash
draftdeck backup /backup/draftdeck-$(date +%F).db   # live, VACUUM INTO
draftdeck check                                     # integrity probe
```

## Security

Uploads are validated against a strict policy (no forms/iframes/embeds,
no external scripts or `on*` handlers, no JS URLs, no meta-refresh — inline
classic `<script>` and inline CSS allowed), and drafts are served with
`script-src 'none'` CSP on isolated per-draft origins. Team drafts are
member-only; anonymous uploads are unlisted and unowned.

## Tests

```bash
go test ./...   # policy, db, and full httptest API suite (upload/raw/versioning/
                # privacy/comments/status/budget/backup-restore)
```

## Layout

```
cmd/draftdeck        server (serve | backup | check subcommands)
cmd/draftdeck-cli    the CLI (npx distributes this binary)
internal/db          SQLite (WAL), schema, migrations, backup
internal/policy      upload-time HTML security policy
internal/server      HTTP API + draft serving + rate limiting
internal/web         session cookies + nordic dashboard (html/template)
internal/store       filesystem object store (S3-shaped keys)
npx/                 the npm bootstrap (downloads the native binary)
.github/workflows    test + release binaries + publish container to GHCR
```
