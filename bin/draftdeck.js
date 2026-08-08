#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import { Command } from "commander";
import { validateHtml } from "../src/html-policy.js";

const { version: VERSION } = createRequire(import.meta.url)("../package.json");
const DEFAULT_API_URL = process.env.DRAFTDECK_API_URL || "http://localhost:4000";
const DECK_DIR = path.join(os.homedir(), ".draftdeck");
const CONFIG_PATH = path.join(DECK_DIR, "config.json");
const CREDENTIALS_PATH = path.join(DECK_DIR, "credentials.json");
const DRAFTS_PATH = path.join(DECK_DIR, "drafts.json");

class CliError extends Error {}

const program = new Command();

program
  .name("draftdeck")
  .description("Publish static HTML drafts and manage team review.")
  .version(VERSION);

const authCommand = program.command("auth").description("Manage CLI authentication.");

authCommand
  .command("set")
  .argument("<api-key>", "draftdeck API key")
  .option("--api-url <url>", "Override the API base URL")
  .action((apiKey, options) => {
    saveCredentials(apiKey, options.apiUrl);
    console.log("draftdeck credentials saved.");
  });

authCommand
  .command("whoami")
  .description("Check the configured credentials.")
  .action(async () => {
    const { apiUrl, apiKey } = readAuth();
    const body = await api(`${apiUrl}/api/me`, { headers: authHeaders(apiKey) });
    console.log(`Account: ${body.accountName} (${body.accountId})`);
    console.log(`API key: ${body.apiKeyName} (${body.apiKeyId})`);
  });

program
  .command("upload")
  .argument("<file>", "HTML file path")
  .option("--draft <draft-id>", "Update a specific draft")
  .option("--new", "Always create a new draft")
  .option("--description <text>", "Short description for the draft")
  .option("--visibility <v>", "public, unlisted, or team (default: unlisted)")
  .option("--team <team-id>", "Attach the draft to a team (implies --visibility team)")
  .option("--api-url <url>", "Override the API base URL")
  .description("Upload or update an HTML draft.")
  .action(async (file, options) => {
    const resolvedFile = path.resolve(file);
    const { apiUrl, apiKey } = readAuth(options.apiUrl, { requireApiKey: false });

    if (!fs.existsSync(resolvedFile)) {
      throw new CliError(`File does not exist: ${resolvedFile}`);
    }

    const html = fs.readFileSync(resolvedFile, "utf8");
    const validation = validateHtml(html);
    if (!validation.ok) {
      throw new CliError(`HTML failed validation:\n- ${validation.errors.join("\n- ")}`);
    }

    const drafts = readDrafts();
    const knownDraft = drafts.files?.[resolvedFile];
    const draftId = options.new ? null : options.draft || knownDraft?.draftId || null;

    const payload = {
      html,
      filename: path.basename(resolvedFile),
      draftId,
      description: options.description,
      visibility: options.team ? "team" : options.visibility,
      teamId: options.team,
      metadata: {
        ...collectGitMetadata(path.dirname(resolvedFile)),
        cliVersion: VERSION
      }
    };

    const headers = { "Content-Type": "application/json", "User-Agent": `draftdeck/${VERSION}` };
    if (apiKey) headers.Authorization = `Bearer ${apiKey}`;

    const body = await api(`${apiUrl}/api/uploads`, {
      method: "POST",
      headers,
      body: JSON.stringify(payload),
      okStatuses: new Set([200, 201])
    });

    drafts.files ||= {};
    drafts.files[resolvedFile] = {
      draftId: body.draftId,
      publicUrl: body.publicUrl,
      rawUrl: body.rawUrl,
      latestVersionNumber: body.versionNumber,
      updatedAt: new Date().toISOString()
    };
    writeJson(DRAFTS_PATH, drafts, 0o600);

    console.log(draftId ? "Updated draft" : "Uploaded draft");
    console.log(`URL: ${body.publicUrl}`);
    console.log(`Raw HTML: ${body.rawUrl}`);
    console.log(`Draft ID: ${body.draftId}`);
    console.log(`Version: ${body.versionNumber} · status: ${body.status}`);
    for (const warning of body.warnings || []) console.warn(`Warning: ${warning}`);
  });

program
  .command("list")
  .description("List drafts on your account and teams.")
  .option("--json", "Print raw JSON")
  .option("--api-url <url>", "Override the API base URL")
  .action(async (options) => {
    const { apiUrl, apiKey } = readAuth(options.apiUrl);
    const body = await api(`${apiUrl}/api/drafts`, { headers: authHeaders(apiKey) });
    const drafts = body.drafts || [];

    if (options.json) {
      console.log(JSON.stringify(drafts, null, 2));
      return;
    }
    if (!drafts.length) {
      console.log("No drafts yet. Publish one with: draftdeck upload <file>");
      return;
    }

    console.log(`Drafts (${drafts.length})\n`);
    for (const d of drafts) {
      const repo = d.repoOrg && d.repoName ? `${d.repoOrg}/${d.repoName}` : "no repo";
      console.log(d.title || "Untitled Draft");
      console.log(`  ${repo} · v${d.latestVersionNumber || "—"} · ${d.versionCount} version${d.versionCount === 1 ? "" : "s"} · ${d.status} · ${timeAgo(d.updatedAt)}`);
      console.log(`  ${d.publicUrl}`);
      if (d.description) console.log(`  ${d.description}`);
      console.log("");
    }
  });

program
  .command("comments")
  .description("List or post comments on a draft (the agent's feedback channel).")
  .argument("<draft-id>", "Draft id")
  .option("--post <text>", "Post a comment with this body")
  .option("--selector <css>", "Anchor the comment to a CSS selector")
  .option("--version <n>", "Comment on a specific version (default: latest)")
  .option("--api-url <url>", "Override the API base URL")
  .action(async (draftId, options) => {
    const { apiUrl, apiKey } = readAuth(options.apiUrl, { requireApiKey: false });

    if (options.post) {
      const headers = { "Content-Type": "application/json" };
      if (apiKey) headers.Authorization = `Bearer ${apiKey}`;
      const payload = {
        body: options.post,
        anchor: options.selector ? { selector: options.selector } : null,
        versionNumber: options.version ? Number(options.version) : undefined
      };
      const body = await api(`${apiUrl}/api/drafts/${draftId}/comments`, {
        method: "POST",
        headers,
        body: JSON.stringify(payload),
        okStatuses: new Set([201])
      });
      console.log(`Comment posted by ${body.comment.author} (v${body.comment.version_number})`);
      return;
    }

    const headers = {};
    if (apiKey) headers.Authorization = `Bearer ${apiKey}`;
    const body = await api(`${apiUrl}/api/drafts/${draftId}/comments`, { headers });
    const comments = body.comments || [];
    if (!comments.length) {
      console.log("No comments yet.");
      return;
    }
    for (const c of comments) {
      const anchor = c.anchor ? ` @ ${anchorLabel(c.anchor)}` : "";
      console.log(`[v${c.version_number}] ${c.author} (${c.created_at})${anchor}`);
      console.log(`  ${c.body}\n`);
    }
  });

program
  .command("status")
  .description("Set a draft's review status: draft | in_review | changes_requested | approved.")
  .argument("<draft-id>", "Draft id")
  .argument("<status>", "New status")
  .option("--api-url <url>", "Override the API base URL")
  .action(async (draftId, status, options) => {
    const { apiUrl, apiKey } = readAuth(options.apiUrl);
    const allowed = new Set(["draft", "in_review", "changes_requested", "approved"]);
    if (!allowed.has(status)) {
      throw new CliError(`Invalid status "${status}". Use one of: ${[...allowed].join(", ")}`);
    }
    const body = await api(`${apiUrl}/api/drafts/${draftId}/status`, {
      method: "POST",
      headers: { ...authHeaders(apiKey), "Content-Type": "application/json" },
      body: JSON.stringify({ status })
    });
    console.log(`${body.draft.title} → ${body.draft.status}`);
  });

program
  .command("teams")
  .description("Create a team and add members.")
  .argument("[action]", "create | members")
  .option("--name <name>", "Team name (for create)")
  .option("--team <team-id>", "Team id (for members)")
  .option("--email <email>", "Member email (for members)")
  .option("--api-url <url>", "Override the API base URL")
  .action(async (action, options) => {
    const { apiUrl, apiKey } = readAuth(options.apiUrl);

    if (action === "create") {
      if (!options.name) throw new CliError("--name is required to create a team.");
      const body = await api(`${apiUrl}/api/teams`, {
        method: "POST",
        headers: { ...authHeaders(apiKey), "Content-Type": "application/json" },
        body: JSON.stringify({ name: options.name })
      });
      console.log(`Team created: ${body.team.name} (${body.team.id})`);
      return;
    }

    if (action === "members") {
      if (!options.team || !options.email) {
        throw new CliError("--team and --email are required to add a member.");
      }
      const body = await api(`${apiUrl}/api/teams/${options.team}/members`, {
        method: "POST",
        headers: { ...authHeaders(apiKey), "Content-Type": "application/json" },
        body: JSON.stringify({ email: options.email })
      });
      console.log(`Added ${body.member.name} (${body.member.email}) to the team.`);
      return;
    }

    const body = await api(`${apiUrl}/api/teams`, { headers: authHeaders(apiKey) });
    for (const t of body.teams || []) console.log(`${t.id}  ${t.name}`);
  });

program.exitOverride();

program.parseAsync(process.argv).catch((error) => {
  if (error instanceof CliError) {
    console.error(error.message);
    process.exit(1);
  }
  if (error.code === "commander.helpDisplayed" || error.code === "commander.version") {
    process.exit(0);
  }
  console.error(error.message || error);
  process.exit(1);
});

// ---- helpers ----------------------------------------------------------------

async function api(url, { method = "GET", headers = {}, body, okStatuses = new Set([200]) } = {}) {
  let response;
  try {
    response = await fetch(url, { method, headers, body, signal: AbortSignal.timeout(30_000) });
  } catch (error) {
    throw new CliError(`Request failed: ${error.message}`);
  }
  const json = await response.json().catch(() => ({}));
  if (!response.ok && !okStatuses.has(response.status)) {
    const details = json.errors?.length ? `\n- ${json.errors.join("\n- ")}` : "";
    throw new CliError(`${json.error || `HTTP ${response.status}`}${details}`);
  }
  return json;
}

function authHeaders(apiKey) {
  return { Authorization: `Bearer ${apiKey}` };
}

function readAuth(apiUrlOverride, { requireApiKey = true } = {}) {
  const config = readJson(CONFIG_PATH, {});
  const credentials = readJson(CREDENTIALS_PATH, {});
  const apiUrl = (
    apiUrlOverride ||
    process.env.DRAFTDECK_API_URL ||
    config.apiUrl ||
    DEFAULT_API_URL
  ).replace(/\/+$/, "");
  const apiKey = process.env.DRAFTDECK_API_KEY || credentials.apiKey;

  if (requireApiKey && !apiKey) {
    throw new CliError("Missing API key. Run: draftdeck auth set <api-key>");
  }
  return { apiUrl, apiKey };
}

function saveCredentials(apiKey, apiUrlOverride) {
  ensureStateDir();
  if (apiUrlOverride) {
    writeJson(CONFIG_PATH, { ...readJson(CONFIG_PATH, {}), apiUrl: apiUrlOverride.replace(/\/+$/, "") });
  }
  writeJson(CREDENTIALS_PATH, { apiKey, updatedAt: new Date().toISOString() }, 0o600);
}

function readDrafts() {
  return readJson(DRAFTS_PATH, { files: {} });
}

function ensureStateDir() {
  fs.mkdirSync(DECK_DIR, { recursive: true, mode: 0o700 });
}

function readJson(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return fallback;
  }
}

function writeJson(file, value, mode = 0o600) {
  ensureStateDir();
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { mode });
  fs.chmodSync(file, mode);
}

function collectGitMetadata(cwd) {
  const repoRoot = git(["rev-parse", "--show-toplevel"], cwd);
  const remote = git(["config", "--get", "remote.origin.url"], cwd);
  const parsedRemote = parseRemote(remote);
  const status = git(["status", "--porcelain"], cwd);

  return {
    repoOrg: parsedRemote.org || (repoRoot ? path.basename(path.dirname(repoRoot)) : null),
    repoName: parsedRemote.name || (repoRoot ? path.basename(repoRoot) : null),
    gitBranch: git(["rev-parse", "--abbrev-ref", "HEAD"], cwd),
    gitCommitSha: git(["rev-parse", "HEAD"], cwd),
    gitCommitSubject: git(["log", "-1", "--format=%s"], cwd),
    gitDirty: status === null ? null : status.length > 0
  };
}

function git(args, cwd) {
  try {
    return execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  } catch {
    return null;
  }
}

function parseRemote(remote) {
  if (!remote) return {};
  const cleaned = remote.replace(/\.git$/, "");
  const sshMatch = cleaned.match(/^[^@]+@([^:]+):([^/]+)\/(.+)$/);
  if (sshMatch) {
    return { host: sshMatch[1], org: sshMatch[2], name: path.basename(sshMatch[3]) };
  }
  try {
    const url = new URL(cleaned);
    const parts = url.pathname.split("/").filter(Boolean);
    if (parts.length >= 2) return { host: url.hostname, org: parts[0], name: parts.at(-1) };
  } catch {
    // fall through
  }
  const parts = cleaned.split("/").filter(Boolean);
  if (parts.length >= 2) return { org: parts.at(-2), name: parts.at(-1) };
  return {};
}

function anchorLabel(anchor) {
  try {
    const a = typeof anchor === "string" ? JSON.parse(anchor) : anchor;
    if (a?.selector) return a.selector;
    if (Number.isFinite(a?.x) && Number.isFinite(a?.y)) return `(${a.x}, ${a.y})`;
    return a?.note || "";
  } catch {
    return "";
  }
}

function timeAgo(value) {
  if (!value) return "unknown";
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "unknown";
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
  const units = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60]
  ];
  for (const [name, secs] of units) {
    const amount = Math.floor(seconds / secs);
    if (amount >= 1) return `${amount} ${name}${amount === 1 ? "" : "s"} ago`;
  }
  return "just now";
}
