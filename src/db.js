import fs from "node:fs";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { config } from "./config.js";
import { sha256, randomToken } from "./crypto.js";
import { newDraftId, newInternalId } from "./ids.js";

export const DB_PATH = path.join(config.dataDir, "draftdeck.db");

let db;

export function getDb() {
  if (!db) {
    fs.mkdirSync(config.dataDir, { recursive: true, mode: 0o700 });
    db = new DatabaseSync(DB_PATH);
    db.exec("PRAGMA journal_mode = WAL;");
    db.exec("PRAGMA foreign_keys = ON;");
    initSchema(db);
  }
  return db;
}

export function closeDb() {
  if (db) {
    db.close();
    db = null;
  }
}

function initSchema(database) {
  database.exec(`
    CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      email TEXT,
      created_at TEXT NOT NULL DEFAULT (datetime('now'))
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL REFERENCES accounts(id),
      name TEXT NOT NULL,
      key_hash TEXT NOT NULL UNIQUE,
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      last_used_at TEXT,
      revoked_at TEXT
    );

    CREATE TABLE IF NOT EXISTS teams (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      created_at TEXT NOT NULL DEFAULT (datetime('now'))
    );

    CREATE TABLE IF NOT EXISTS team_members (
      team_id TEXT NOT NULL REFERENCES teams(id),
      account_id TEXT NOT NULL REFERENCES accounts(id),
      role TEXT NOT NULL DEFAULT 'member',
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      PRIMARY KEY (team_id, account_id)
    );

    -- visibility: 'public' (listed + served raw to anyone),
    --             'unlisted' (random URL, served raw to anyone, not listed),
    --             'team' (only authenticated members of team_id can view/edit)
    -- status:    'draft' | 'in_review' | 'changes_requested' | 'approved'
    CREATE TABLE IF NOT EXISTS drafts (
      id TEXT PRIMARY KEY,
      account_id TEXT REFERENCES accounts(id),
      team_id TEXT REFERENCES teams(id),
      title TEXT NOT NULL,
      description TEXT,
      visibility TEXT NOT NULL DEFAULT 'unlisted',
      status TEXT NOT NULL DEFAULT 'draft',
      current_version_id TEXT,
      repo_org TEXT,
      repo_name TEXT,
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at TEXT NOT NULL DEFAULT (datetime('now')),
      deleted_at TEXT
    );

    CREATE TABLE IF NOT EXISTS draft_versions (
      id TEXT PRIMARY KEY,
      draft_id TEXT NOT NULL REFERENCES drafts(id),
      version_number INTEGER NOT NULL,
      object_key TEXT NOT NULL,
      content_hash TEXT NOT NULL,
      file_size INTEGER NOT NULL,
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      created_by_api_key_id TEXT REFERENCES api_keys(id),
      source_ip TEXT,
      user_agent TEXT,
      cli_version TEXT,
      git_branch TEXT,
      git_commit_sha TEXT,
      git_commit_subject TEXT,
      git_dirty INTEGER,
      original_filename TEXT,
      UNIQUE (draft_id, version_number)
    );

    CREATE TABLE IF NOT EXISTS comments (
      id TEXT PRIMARY KEY,
      draft_id TEXT NOT NULL REFERENCES drafts(id),
      version_number INTEGER NOT NULL,
      -- JSON: { selector?: string, x?: number, y?: number, note?: string }
      anchor TEXT,
      body TEXT NOT NULL,
      author TEXT NOT NULL,
      created_at TEXT NOT NULL DEFAULT (datetime('now'))
    );

    CREATE INDEX IF NOT EXISTS idx_versions_draft ON draft_versions(draft_id);
    CREATE INDEX IF NOT EXISTS idx_comments_draft ON comments(draft_id);
    CREATE INDEX IF NOT EXISTS idx_drafts_team ON drafts(team_id);
    CREATE INDEX IF NOT EXISTS idx_drafts_account ON drafts(account_id);
  `);

  // The anonymous sentinel: a real account + api_key row so anonymous uploads
  // satisfy the created_by_api_key_id foreign key (same trick as postplan's
  // public-upload sentinel). Its key_hash is a constant, never derivable from
  // a usable token.
  database.exec(`
    INSERT OR IGNORE INTO accounts (id, name) VALUES ('acct_anonymous', 'Anonymous');
    INSERT OR IGNORE INTO api_keys (id, account_id, name, key_hash)
      VALUES ('key_anonymous', 'acct_anonymous', 'Anonymous', '${sha256("draftdeck-anonymous-sentinel")}');
  `);
}

// ---- auth -----------------------------------------------------------------

// A sentinel identity used for anonymous uploads/views so every version row
// still has a creator. Never stored as a real api_keys row we authenticate.
export const anonymousAuth = {
  id: "key_anonymous",
  account_id: "acct_anonymous",
  name: "Anonymous",
  account_name: "Anonymous"
};

export function createAccount(name, email = null) {
  const accountId = `acct_${newInternalId()}`;
  getDb().prepare("INSERT INTO accounts (id, name, email) VALUES (?, ?, ?)").run(accountId, name, email ? String(email).toLowerCase() : null);
  return accountId;
}

export function createApiKey(accountId, name) {
  const token = `dd_${randomToken(32)}`;
  const apiKeyId = `key_${newInternalId()}`;
  getDb()
    .prepare("INSERT INTO api_keys (id, account_id, name, key_hash) VALUES (?, ?, ?, ?)")
    .run(apiKeyId, accountId, name, sha256(token));
  return { id: apiKeyId, token };
}

export function findApiKeyByToken(token) {
  const row = getDb()
    .prepare(
      `SELECT k.id, k.account_id, k.name, a.name AS account_name
       FROM api_keys k JOIN accounts a ON a.id = k.account_id
       WHERE k.key_hash = ? AND k.revoked_at IS NULL AND k.id <> ?
       LIMIT 1`
    )
    .get(sha256(token), anonymousAuth.id);
  if (!row) return null;
  getDb().prepare("UPDATE api_keys SET last_used_at = datetime('now') WHERE id = ?").run(row.id);
  return row;
}

// ---- drafts ---------------------------------------------------------------

export function createDraft({ accountId, teamId, title, description, visibility }) {
  const id = newDraftId();
  getDb()
    .prepare(
      `INSERT INTO drafts (id, account_id, team_id, title, description, visibility)
       VALUES (?, ?, ?, ?, ?, ?)`
    )
    .run(id, accountId, teamId, title, description, visibility || "unlisted");
  return id;
}

export function findDraft(draftId) {
  return getDb().prepare("SELECT * FROM drafts WHERE id = ?").get(draftId) || null;
}

export function getDraftDetail(draftId) {
  const db = getDb();
  const draft = db.prepare("SELECT * FROM drafts WHERE id = ? AND deleted_at IS NULL").get(draftId);
  if (!draft) return null;
  const versions = db
    .prepare(
      `SELECT id, version_number, created_at, file_size, git_branch, git_commit_sha,
              git_commit_subject, git_dirty, original_filename, cli_version
       FROM draft_versions WHERE draft_id = ? ORDER BY version_number DESC`
    )
    .all(draftId);
  const comments = db
    .prepare(
      `SELECT id, version_number, anchor, body, author, created_at
       FROM comments WHERE draft_id = ? ORDER BY created_at ASC`
    )
    .all(draftId);
  return { draft, versions, comments };
}

export function listDraftsForAccount(accountId) {
  // Own drafts + drafts of teams the account belongs to. Public drafts are
  // excluded from listings (they're reachable by URL, not by browsing).
  const rows = getDb()
    .prepare(
      `SELECT DISTINCT d.*, COALESCE(cv.version_number, 0) AS latest_version_number,
              (SELECT COUNT(*) FROM draft_versions v2 WHERE v2.draft_id = d.id) AS version_count
       FROM drafts d
       LEFT JOIN draft_versions cv ON cv.id = d.current_version_id
       LEFT JOIN team_members tm ON tm.team_id = d.team_id
       WHERE d.deleted_at IS NULL
         AND d.visibility != 'public'
         AND (d.account_id = ? OR tm.account_id = ?)
       ORDER BY d.updated_at DESC`
    )
    .all(accountId, accountId);
  return rows.map(publicDraftRow);
}

export function listTeamDrafts(teamId) {
  const rows = getDb()
    .prepare(
      `SELECT d.*, COALESCE(cv.version_number, 0) AS latest_version_number,
              (SELECT COUNT(*) FROM draft_versions v2 WHERE v2.draft_id = d.id) AS version_count
       FROM drafts d
       LEFT JOIN draft_versions cv ON cv.id = d.current_version_id
       WHERE d.team_id = ? AND d.deleted_at IS NULL
       ORDER BY d.updated_at DESC`
    )
    .all(teamId);
  return rows.map(publicDraftRow);
}

function publicDraftRow(row) {
  return {
    draftId: row.id,
    title: row.title,
    description: row.description,
    visibility: row.visibility,
    status: row.status,
    teamId: row.team_id,
    repoOrg: row.repo_org,
    repoName: row.repo_name,
    latestVersionNumber: Number(row.latest_version_number),
    versionCount: Number(row.version_count),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    publicUrl: `${config.publicBaseUrl}/d/${row.id}`,
    rawUrl: `${config.publicBaseUrl}/d/${row.id}/raw`
  };
}

export function addVersion({ draftId, versionId, objectKey, contentHash, fileSize, apiKeyId, meta }) {
  getDb()
    .prepare(
      `INSERT INTO draft_versions (
         id, draft_id, version_number, object_key, content_hash, file_size,
         created_by_api_key_id, source_ip, user_agent, cli_version,
         git_branch, git_commit_sha, git_commit_subject, git_dirty, original_filename
       ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    )
    .run(
      versionId,
      draftId,
      nextVersionNumber(draftId),
      objectKey,
      contentHash,
      fileSize,
      apiKeyId,
      meta.sourceIp || null,
      meta.userAgent || null,
      meta.cliVersion || null,
      meta.gitBranch || null,
      meta.gitCommitSha || null,
      meta.gitCommitSubject || null,
      meta.gitDirty == null ? null : meta.gitDirty ? 1 : 0,
      meta.originalFilename || null
    );
  const version = getDb()
    .prepare("SELECT * FROM draft_versions WHERE id = ?")
    .get(versionId);
  return version;
}

function nextVersionNumber(draftId) {
  const row = getDb()
    .prepare("SELECT COALESCE(MAX(version_number), 0) + 1 AS n FROM draft_versions WHERE draft_id = ?")
    .get(draftId);
  return Number(row.n);
}

export function updateDraftCurrent(draftId, versionId, { title, description }) {
  getDb()
    .prepare(
      `UPDATE drafts
       SET current_version_id = ?, title = COALESCE(?, title),
           description = COALESCE(?, description), updated_at = datetime('now')
       WHERE id = ?`
    )
    .run(versionId, title || null, description || null, draftId);
}

export function getVersion(draftId, versionNumber) {
  return (
    getDb()
      .prepare("SELECT * FROM draft_versions WHERE draft_id = ? AND version_number = ?")
      .get(draftId, versionNumber) || null
  );
}

export function getCurrentVersion(draftId) {
  const draft = findDraft(draftId);
  if (!draft?.current_version_id) return null;
  return getDb().prepare("SELECT * FROM draft_versions WHERE id = ?").get(draft.current_version_id) || null;
}

export function setDraftStatus(draftId, status) {
  const allowed = new Set(["draft", "in_review", "changes_requested", "approved"]);
  if (!allowed.has(status)) return null;
  getDb()
    .prepare("UPDATE drafts SET status = ?, updated_at = datetime('now') WHERE id = ?")
    .run(status, draftId);
  return findDraft(draftId);
}

export function softDeleteDraft(draftId) {
  getDb().prepare("UPDATE drafts SET deleted_at = datetime('now') WHERE id = ?").run(draftId);
}

// ---- comments -------------------------------------------------------------

export function addComment({ draftId, versionNumber, anchor, body, author }) {
  const id = `cmt_${newInternalId()}`;
  getDb()
    .prepare(
      `INSERT INTO comments (id, draft_id, version_number, anchor, body, author)
       VALUES (?, ?, ?, ?, ?, ?)`
    )
    .run(id, draftId, versionNumber, anchor ? JSON.stringify(anchor) : null, body, author);
  return getDb().prepare("SELECT * FROM comments WHERE id = ?").get(id);
}

// ---- teams ----------------------------------------------------------------

export function createTeam(name, ownerAccountId) {
  const id = `team_${newInternalId()}`;
  getDb().prepare("INSERT INTO teams (id, name) VALUES (?, ?)").run(id, name);
  getDb()
    .prepare("INSERT INTO team_members (team_id, account_id, role) VALUES (?, ?, 'owner')")
    .run(id, ownerAccountId);
  return getDb().prepare("SELECT * FROM teams WHERE id = ?").get(id);
}

export function addTeamMember(teamId, accountId, role = "member") {
  getDb()
    .prepare("INSERT OR IGNORE INTO team_members (team_id, account_id, role) VALUES (?, ?, ?)")
    .run(teamId, accountId, role);
}

export function isTeamMember(teamId, accountId) {
  return Boolean(
    getDb().prepare("SELECT 1 FROM team_members WHERE team_id = ? AND account_id = ?").get(teamId, accountId)
  );
}

export function isTeamOwner(teamId, accountId) {
  const row = getDb()
    .prepare("SELECT role FROM team_members WHERE team_id = ? AND account_id = ?")
    .get(teamId, accountId);
  return row?.role === "owner";
}

export function listTeamsForAccount(accountId) {
  return getDb()
    .prepare(
      `SELECT t.* FROM teams t
       JOIN team_members tm ON tm.team_id = t.id
       WHERE tm.account_id = ?
       ORDER BY t.created_at DESC`
    )
    .all(accountId);
}

export function findTeam(teamId) {
  return getDb().prepare("SELECT * FROM teams WHERE id = ?").get(teamId) || null;
}
