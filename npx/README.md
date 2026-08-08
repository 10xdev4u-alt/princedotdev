# draftdeck — npx client

```bash
npx draftdeck upload ./plan.html
npx draftdeck comments <draft-id>
npx draftdeck status <draft-id> approved
```

This package is a tiny bootstrap: on first run it downloads the real client —
a single static Go binary — from GitHub Releases for your platform
(`draftdeck-linux-amd64`, `draftdeck-darwin-arm64`, …) and caches it in
`~/.cache/draftdeck/`. Subsequent runs are instant, no runtime dependencies.

## Configuration

| Env var | Purpose |
| --- | --- |
| `DRAFTDECK_API_URL` | API base URL (default `http://localhost:4000`) |
| `DRAFTDECK_API_KEY` | API key override |
| `DRAFTDECK_HOME` | CLI state dir (default `~/.draftdeck`) |
| `DRAFTDECK_BIN_BASE` | Binary download mirror override |
| `DRAFTDECK_CACHE` | Binary cache dir (default `~/.cache/draftdeck`) |
