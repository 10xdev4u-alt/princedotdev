// Package db is the SQLite persistence layer (pure-Go modernc driver, no CGO).
// The schema mirrors the Node implementation so the API and CLI port 1:1.
package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
)

// Sentinel identities. Anonymous uploads are attributed to a real account row
// so the created_by_api_key_id foreign key always holds (same trick as the
// Node version and postplan).
const (
	AnonymousAccountID = "acct_anonymous"
	AnonymousKeyID     = "key_anonymous"
	AnonymousName      = "Anonymous"
)

const schema = `
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
  created_by_api_key_id TEXT NOT NULL REFERENCES api_keys(id),
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
  anchor TEXT,
  body TEXT NOT NULL,
  author TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS draft_tags (
  draft_id TEXT NOT NULL REFERENCES drafts(id),
  tag TEXT NOT NULL,
  PRIMARY KEY (draft_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_tags_tag ON draft_tags(tag);
CREATE TABLE IF NOT EXISTS draft_approvals (
  draft_id TEXT NOT NULL REFERENCES drafts(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (draft_id, account_id)
);
CREATE TABLE IF NOT EXISTS draft_reviewers (
  draft_id TEXT NOT NULL REFERENCES drafts(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  added_by TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (draft_id, account_id)
);
CREATE INDEX IF NOT EXISTS idx_reviewers_draft ON draft_reviewers(draft_id);
CREATE TABLE IF NOT EXISTS activity (
  id TEXT PRIMARY KEY,
  account_id TEXT REFERENCES accounts(id),
  team_id TEXT REFERENCES teams(id),
  draft_id TEXT REFERENCES drafts(id),
  kind TEXT NOT NULL,
  actor TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_activity_account ON activity(account_id);
CREATE INDEX IF NOT EXISTS idx_activity_team ON activity(team_id);
CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL REFERENCES teams(id),
  email TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT NOT NULL,
  used_at TEXT
);
CREATE TABLE IF NOT EXISTS webhooks (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id),
  team_id TEXT REFERENCES teams(id),
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'discord',
  url TEXT NOT NULL,
  events TEXT NOT NULL DEFAULT 'comment,status,upload',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  last_status INTEGER,
  last_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_versions_draft ON draft_versions(draft_id);
CREATE INDEX IF NOT EXISTS idx_comments_draft ON comments(draft_id);
CREATE INDEX IF NOT EXISTS idx_drafts_team ON drafts(team_id);
CREATE INDEX IF NOT EXISTS idx_drafts_account ON drafts(account_id);
`

// DB wraps the sql.DB handle with the typed data access used by the API.
type DB struct {
	sql *sql.DB
}

// Open initializes the database (schema + sentinel rows) at cfg.DataDir.
func Open(cfg config.Config) (*DB, error) {
	if err := ensureDir(cfg.DataDir); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(cfg.DataDir, "draftdeck.db") +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // SQLite: single writer, avoid lock contention
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}
	d := &DB{sql: sqlDB}
	if _, err := sqlDB.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Additive column migrations for pre-existing databases.
	if err := d.ensureColumn("accounts", "feed_read_at", "TEXT"); err != nil {
		return nil, err
	}
	if err := d.ensureColumn("teams", "required_approvals", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return nil, err
	}
	if err := d.ensureSentinel(); err != nil {
		return nil, err
	}
	return d, nil
}

// ensureColumn adds a column to an existing table when missing (SQLite lacks
// ADD COLUMN IF NOT EXISTS). Used for additive migrations on old databases.
func (d *DB) ensureColumn(table, column, decl string) error {
	rows, err := d.sql.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt, pk any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = d.sql.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl)
	return err
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func (d *DB) ensureSentinel() error {
	// One statement per Exec: modernc binds args positionally, so a single
	// multi-statement Exec with args would only bind the first statement.
	if _, err := d.sql.Exec(
		`INSERT OR IGNORE INTO accounts (id, name) VALUES (?, ?)`,
		AnonymousAccountID, AnonymousName); err != nil {
		return err
	}
	_, err := d.sql.Exec(
		`INSERT OR IGNORE INTO api_keys (id, account_id, name, key_hash) VALUES (?, ?, ?, ?)`,
		AnonymousKeyID, AnonymousAccountID, AnonymousName, hashToken("draftdeck-anonymous-sentinel"))
	return err
}

// Ping verifies the database is reachable (healthz).
func (d *DB) Ping() error {
	var one int
	return d.sql.QueryRow("SELECT 1").Scan(&one)
}

// Checkpoint flushes the WAL into the main database file (TRUNCATE mode), so
// the .db file on disk is a complete, consistent snapshot. Called on graceful
// shutdown and before backups.
func (d *DB) Checkpoint() error {
	_, err := d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Backup writes a consistent online snapshot of the database to dest using
// VACUUM INTO — safe to run while the server is live (WAL readers/writers
// keep working; the snapshot is transactionally consistent). The destination
// must not already exist.
func (d *DB) Backup(dest string) error {
	escaped := strings.ReplaceAll(dest, "'", "''")
	_, err := d.sql.Exec("VACUUM INTO '" + escaped + "'")
	return err
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.sql.Close() }

// ---- accounts & api keys -----------------------------------------------------

// Account is a human (personal) or bot identity.
type Account struct {
	ID    string
	Name  string
	Email string
}

// APIKey is a key row joined with its account.
type APIKey struct {
	ID          string
	AccountID   string
	Name        string
	AccountName string
	CreatedAt   string
	LastUsedAt  string
}

// CreateAccount inserts an account and returns its id.
func (d *DB) CreateAccount(name, email string) (string, error) {
	id := newID("acct")
	_, err := d.sql.Exec(`INSERT INTO accounts (id, name, email) VALUES (?, ?, ?)`, id, name, lowercase(email))
	return id, err
}

// CreateAPIKey mints a fresh token, stores only its sha256, and returns the
// key id and the plaintext token (shown exactly once).
func (d *DB) CreateAPIKey(accountID, name string) (keyID, token string, err error) {
	token = "dd_" + randomToken(32)
	keyID = newID("key")
	_, err = d.sql.Exec(
		`INSERT INTO api_keys (id, account_id, name, key_hash) VALUES (?, ?, ?, ?)`,
		keyID, accountID, name, hashToken(token))
	return keyID, token, err
}

// FindAPIKeyByToken resolves a bearer token to its key+account, or nil.
// The anonymous sentinel can never be used as a real credential.
func (d *DB) FindAPIKeyByToken(token string) (*APIKey, error) {
	row := d.sql.QueryRow(`
		SELECT k.id, k.account_id, k.name, a.name AS account_name
		FROM api_keys k JOIN accounts a ON a.id = k.account_id
		WHERE k.key_hash = ? AND k.revoked_at IS NULL AND k.id <> ?
		LIMIT 1`, hashToken(token), AnonymousKeyID)
	k := &APIKey{}
	err := row.Scan(&k.ID, &k.AccountID, &k.Name, &k.AccountName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, _ = d.sql.Exec(`UPDATE api_keys SET last_used_at = datetime('now') WHERE id = ?`, k.ID)
	return k, nil
}

// ListAPIKeys lists non-revoked keys for an account.
func (d *DB) ListAPIKeys(accountID string) ([]APIKey, error) {
	rows, err := d.sql.Query(
		`SELECT id, account_id, name, '' AS account_name, COALESCE(created_at,''), COALESCE(last_used_at,'')
		 FROM api_keys WHERE account_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.AccountID, &k.Name, &k.AccountName, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ---- teams ---------------------------------------------------------------------

// Team is a shared workspace for a group of accounts.
type Team struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	CreatedAt         string `json:"createdAt"`
	RequiredApprovals int64  `json:"requiredApprovals"`
}

// CreateTeam makes a team owned by accountID.
func (d *DB) CreateTeam(name, ownerID string) (Team, error) {
	id := newID("team")
	if _, err := d.sql.Exec(`INSERT INTO teams (id, name) VALUES (?, ?)`, id, name); err != nil {
		return Team{}, err
	}
	_, err := d.sql.Exec(
		`INSERT OR IGNORE INTO team_members (team_id, account_id, role) VALUES (?, ?, 'owner')`,
		id, ownerID)
	if err != nil {
		return Team{}, err
	}
	return d.FindTeam(id)
}

// FindTeam returns a team by id.
func (d *DB) FindTeam(id string) (Team, error) {
	row := d.sql.QueryRow(`SELECT id, name, created_at, COALESCE(required_approvals,0) FROM teams WHERE id = ?`, id)
	var t Team
	err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.RequiredApprovals)
	return t, err
}

// AddTeamMember adds (or keeps) a member on a team.
func (d *DB) AddTeamMember(teamID, accountID, role string) error {
	if role == "" {
		role = "member"
	}
	_, err := d.sql.Exec(
		`INSERT OR IGNORE INTO team_members (team_id, account_id, role) VALUES (?, ?, ?)`,
		teamID, accountID, role)
	return err
}

// IsTeamMember reports whether accountID belongs to teamID.
func (d *DB) IsTeamMember(teamID, accountID string) (bool, error) {
	var one int
	err := d.sql.QueryRow(
		`SELECT 1 FROM team_members WHERE team_id = ? AND account_id = ?`,
		teamID, accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// IsTeamOwner reports whether accountID owns teamID (role 'owner').
func (d *DB) IsTeamOwner(teamID, accountID string) (bool, error) {
	var one int
	err := d.sql.QueryRow(
		`SELECT 1 FROM team_members WHERE team_id = ? AND account_id = ? AND role = 'owner'`,
		teamID, accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UpdateTeamApprovals sets the approval-gate count for a team.
func (d *DB) UpdateTeamApprovals(teamID string, n int64) error {
	_, err := d.sql.Exec(`UPDATE teams SET required_approvals = ? WHERE id = ?`, n, teamID)
	return err
}

// IsTeamAdmin reports whether accountID is an admin or owner of teamID.
func (d *DB) IsTeamAdmin(teamID, accountID string) (bool, error) {
	var one int
	err := d.sql.QueryRow(
		`SELECT 1 FROM team_members WHERE team_id = ? AND account_id = ? AND role IN ('owner','admin')`,
		teamID, accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetMemberRole updates a member's role (owner rows are protected).
func (d *DB) SetMemberRole(teamID, accountID, role string) error {
	_, err := d.sql.Exec(
		`UPDATE team_members SET role = ? WHERE team_id = ? AND account_id = ? AND role != 'owner'`,
		role, teamID, accountID)
	return err
}

// AddDraftApproval records an approver for a draft (idempotent per account).
func (d *DB) AddDraftApproval(draftID, accountID string) error {
	_, err := d.sql.Exec(`
		INSERT OR IGNORE INTO draft_approvals (draft_id, account_id) VALUES (?, ?)`,
		draftID, accountID)
	return err
}

// ApprovalCount returns the number of distinct approvers of a draft.
func (d *DB) ApprovalCount(draftID string) (int64, error) {
	var n int64
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM draft_approvals WHERE draft_id = ?`, draftID).Scan(&n)
	return n, err
}

// ReviewerApprovalCount returns approvals from accounts assigned as reviewers.
// When reviewers are assigned, the approval gate counts only their approvals.
func (d *DB) ReviewerApprovalCount(draftID string) (int64, error) {
	var n int64
	err := d.sql.QueryRow(`
		SELECT COUNT(*) FROM draft_approvals a
		JOIN draft_reviewers r ON r.draft_id = a.draft_id AND r.account_id = a.account_id
		WHERE a.draft_id = ?`, draftID).Scan(&n)
	return n, err
}

// SetDraftReviewers replaces the draft's assigned reviewers with accountIDs.
func (d *DB) SetDraftReviewers(draftID string, accountIDs []string, addedBy string) error {
	if _, err := d.sql.Exec(`DELETE FROM draft_reviewers WHERE draft_id = ?`, draftID); err != nil {
		return err
	}
	for _, id := range accountIDs {
		if _, err := d.sql.Exec(`
			INSERT OR IGNORE INTO draft_reviewers (draft_id, account_id, added_by) VALUES (?, ?, ?)`,
			draftID, id, addedBy); err != nil {
			return err
		}
	}
	return nil
}

// ListDraftReviewers returns the assigned reviewers (name + email) of a draft.
func (d *DB) ListDraftReviewers(draftID string) ([]TeamMember, error) {
	rows, err := d.sql.Query(`
		SELECT r.account_id, a.name, COALESCE(a.email,''), ''
		FROM draft_reviewers r JOIN accounts a ON a.id = r.account_id
		WHERE r.draft_id = ? ORDER BY a.name ASC`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.AccountID, &m.Name, &m.Email, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DraftHasReviewers reports whether any reviewers are assigned to the draft.
func (d *DB) DraftHasReviewers(draftID string) (bool, error) {
	var n int64
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM draft_reviewers WHERE draft_id = ?`, draftID).Scan(&n)
	return n > 0, err
}

// ReviewerApprovalStatus returns which assigned reviewers have approved.
func (d *DB) ReviewerApprovalStatus(draftID string) (map[string]bool, error) {
	rows, err := d.sql.Query(`
		SELECT r.account_id, (a.account_id IS NOT NULL)
		FROM draft_reviewers r
		LEFT JOIN draft_approvals a ON a.draft_id = r.draft_id AND a.account_id = r.account_id
		WHERE r.draft_id = ?`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var approved bool
		if err := rows.Scan(&id, &approved); err != nil {
			return nil, err
		}
		out[id] = approved
	}
	return out, rows.Err()
}

// ListTeamDrafts returns the team's drafts (never deleted), newest first.
func (d *DB) ListTeamDrafts(teamID string) ([]DraftListItem, error) {
	rows, err := d.sql.Query(`
		SELECT dr.id, dr.title, COALESCE(dr.description,''), dr.visibility, dr.status,
		       COALESCE(dr.team_id,''), COALESCE(dr.repo_org,''), COALESCE(dr.repo_name,''),
		       COALESCE(cv.version_number,0), COALESCE(vc.version_count,0),
		       dr.created_at, dr.updated_at
		FROM drafts dr
		LEFT JOIN draft_versions cv ON cv.id = dr.current_version_id
		LEFT JOIN (SELECT draft_id, COUNT(*) AS version_count FROM draft_versions GROUP BY draft_id) vc
		       ON vc.draft_id = dr.id
		WHERE dr.team_id = ? AND dr.deleted_at IS NULL
		ORDER BY dr.updated_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DraftListItem
	for rows.Next() {
		var it DraftListItem
		if err := rows.Scan(&it.DraftID, &it.Title, &it.Description, &it.Visibility, &it.Status,
			&it.TeamID, &it.RepoOrg, &it.RepoName, &it.LatestVersionNumber, &it.VersionCount,
			&it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// FindAccountByEmail returns an account by exact (lowercased) email, or nil.
func (d *DB) FindAccountByEmail(email string) (*Account, error) {
	row := d.sql.QueryRow(`SELECT id, name, COALESCE(email,'') FROM accounts WHERE lower(email) = ?`, lowercase(email))
	var a Account
	err := row.Scan(&a.ID, &a.Name, &a.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListTeamsForAccount lists the teams an account belongs to.
func (d *DB) ListTeamsForAccount(accountID string) ([]Team, error) {
	rows, err := d.sql.Query(`
		SELECT t.id, t.name, t.created_at, COALESCE(t.required_approvals,0) FROM teams t
		JOIN team_members tm ON tm.team_id = t.id
		WHERE tm.account_id = ? ORDER BY t.created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.RequiredApprovals); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- drafts ----------------------------------------------------------------------

// Draft is a draft row.
type Draft struct {
	ID               string
	AccountID        string
	TeamID           string
	Title            string
	Description      string
	Visibility       string
	Status           string
	CurrentVersionID string
	RepoOrg          string
	RepoName         string
	CreatedAt        string
	UpdatedAt        string
	DeletedAt        string
}

// Version is a draft_versions row.
type Version struct {
	ID               string
	DraftID          string
	VersionNumber    int64
	ObjectKey        string
	ContentHash      string
	FileSize         int64
	CreatedAt        string
	GitBranch        string
	GitCommitSHA     string
	GitCommitSubject string
	GitDirty         bool
	OriginalFilename string
	CLIVersion       string
	// Author is the publishing account name (audit trail, joined in).
	Author string
}

// Comment is a comment row.
type Comment struct {
	ID            string
	DraftID       string
	VersionNumber int64
	Anchor        string
	Body          string
	Author        string
	CreatedAt     string
}

// CreateDraft inserts a draft and returns its id.
func (d *DB) CreateDraft(accountID, teamID, title, description, visibility string) (string, error) {
	id := newID("")
	_, err := d.sql.Exec(`
		INSERT INTO drafts (id, account_id, team_id, title, description, visibility)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, nullIfEmpty(accountID), nullIfEmpty(teamID), title, description, orDefault(visibility, "unlisted"))
	return id, err
}

// FindDraft returns a draft row (including soft-deleted, caller checks).
func (d *DB) FindDraft(id string) (*Draft, error) {
	row := d.sql.QueryRow(`
		SELECT id, COALESCE(account_id,''), COALESCE(team_id,''), title, COALESCE(description,''),
		       visibility, status, COALESCE(current_version_id,''), COALESCE(repo_org,''),
		       COALESCE(repo_name,''), created_at, updated_at, COALESCE(deleted_at,'')
		FROM drafts WHERE id = ?`, id)
	var dr Draft
	err := row.Scan(&dr.ID, &dr.AccountID, &dr.TeamID, &dr.Title, &dr.Description, &dr.Visibility,
		&dr.Status, &dr.CurrentVersionID, &dr.RepoOrg, &dr.RepoName, &dr.CreatedAt, &dr.UpdatedAt, &dr.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dr, nil
}

// DraftListItem is a listing row with version aggregates.
type DraftListItem struct {
	DraftID             string
	Title               string
	Description         string
	Visibility          string
	Status              string
	TeamID              string
	RepoOrg             string
	RepoName            string
	LatestVersionNumber int64
	VersionCount        int64
	CreatedAt           string
	UpdatedAt           string
}

// ListDraftsForAccount returns own + team drafts (never public, never deleted).
func (d *DB) ListDraftsForAccount(accountID string) ([]DraftListItem, error) {
	rows, err := d.sql.Query(`
		SELECT DISTINCT dr.id, dr.title, COALESCE(dr.description,''), dr.visibility, dr.status,
		       COALESCE(dr.team_id,''), COALESCE(dr.repo_org,''), COALESCE(dr.repo_name,''),
		       COALESCE(cv.version_number,0), COALESCE(vc.version_count,0),
		       dr.created_at, dr.updated_at
		FROM drafts dr
		LEFT JOIN draft_versions cv ON cv.id = dr.current_version_id
		LEFT JOIN (SELECT draft_id, COUNT(*) AS version_count FROM draft_versions GROUP BY draft_id) vc
		       ON vc.draft_id = dr.id
		LEFT JOIN team_members tm ON tm.team_id = dr.team_id
		WHERE dr.deleted_at IS NULL AND dr.visibility != 'public'
		  AND (dr.account_id = ? OR tm.account_id = ?)
		ORDER BY dr.updated_at DESC`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DraftListItem
	for rows.Next() {
		var it DraftListItem
		if err := rows.Scan(&it.DraftID, &it.Title, &it.Description, &it.Visibility, &it.Status,
			&it.TeamID, &it.RepoOrg, &it.RepoName, &it.LatestVersionNumber, &it.VersionCount,
			&it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// NextVersionNumber returns max(version_number)+1 for a draft.
func (d *DB) NextVersionNumber(draftID string) (int64, error) {
	var n int64
	err := d.sql.QueryRow(
		`SELECT COALESCE(MAX(version_number),0)+1 FROM draft_versions WHERE draft_id = ?`,
		draftID).Scan(&n)
	return n, err
}

// AddVersion stores a version row and returns it.
func (d *DB) AddVersion(v Version, createdByKeyID, sourceIP, userAgent string) (Version, error) {
	v.ID = newID("ver")
	v.VersionNumber, _ = d.NextVersionNumber(v.DraftID)
	_, err := d.sql.Exec(`
		INSERT INTO draft_versions (
			id, draft_id, version_number, object_key, content_hash, file_size,
			created_by_api_key_id, source_ip, user_agent, cli_version,
			git_branch, git_commit_sha, git_commit_subject, git_dirty, original_filename)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.DraftID, v.VersionNumber, v.ObjectKey, v.ContentHash, v.FileSize,
		createdByKeyID, nullIfEmpty(sourceIP), nullIfEmpty(userAgent), nullIfEmpty(v.CLIVersion),
		nullIfEmpty(v.GitBranch), nullIfEmpty(v.GitCommitSHA), nullIfEmpty(v.GitCommitSubject),
		boolInt(v.GitDirty), nullIfEmpty(v.OriginalFilename))
	return v, err
}

// SetCurrentVersion updates the draft's current version pointer + title/description.
func (d *DB) SetCurrentVersion(draftID, versionID, title, description string) error {
	_, err := d.sql.Exec(`
		UPDATE drafts SET current_version_id = ?, title = COALESCE(?, title),
		       description = COALESCE(?, description), updated_at = datetime('now')
		WHERE id = ?`, versionID, nullIfEmpty(title), nullIfEmpty(description), draftID)
	return err
}

// GetVersion returns a specific version of a draft.
func (d *DB) GetVersion(draftID string, versionNumber int64) (*Version, error) {
	row := d.sql.QueryRow(`
		SELECT id, draft_id, version_number, object_key, content_hash, file_size, created_at,
		       COALESCE(git_branch,''), COALESCE(git_commit_sha,''), COALESCE(git_commit_subject,''),
		       COALESCE(git_dirty,0), COALESCE(original_filename,''), COALESCE(cli_version,'')
		FROM draft_versions WHERE draft_id = ? AND version_number = ?`, draftID, versionNumber)
	return scanVersion(row)
}

// GetCurrentVersion returns the draft's current version.
func (d *DB) GetCurrentVersion(draftID string) (*Version, error) {
	row := d.sql.QueryRow(`
		SELECT v.id, v.draft_id, v.version_number, v.object_key, v.content_hash, v.file_size, v.created_at,
		       COALESCE(v.git_branch,''), COALESCE(v.git_commit_sha,''), COALESCE(v.git_commit_subject,''),
		       COALESCE(v.git_dirty,0), COALESCE(v.original_filename,''), COALESCE(v.cli_version,'')
		FROM draft_versions v JOIN drafts dr ON dr.current_version_id = v.id
		WHERE dr.id = ?`, draftID)
	return scanVersion(row)
}

func scanVersion(row *sql.Row) (*Version, error) {
	var v Version
	var dirty int
	err := row.Scan(&v.ID, &v.DraftID, &v.VersionNumber, &v.ObjectKey, &v.ContentHash, &v.FileSize,
		&v.CreatedAt, &v.GitBranch, &v.GitCommitSHA, &v.GitCommitSubject, &dirty,
		&v.OriginalFilename, &v.CLIVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.GitDirty = dirty != 0
	return &v, nil
}

// ListVersions lists all versions of a draft, newest first.
func (d *DB) ListVersions(draftID string) ([]Version, error) {
	rows, err := d.sql.Query(`
		SELECT v.id, v.draft_id, v.version_number, v.object_key, v.content_hash, v.file_size, v.created_at,
		       COALESCE(v.git_branch,''), COALESCE(v.git_commit_sha,''), COALESCE(v.git_commit_subject,''),
		       COALESCE(v.git_dirty,0), COALESCE(v.original_filename,''), COALESCE(v.cli_version,''),
		       COALESCE(a.name,'')
		FROM draft_versions v
		LEFT JOIN api_keys k ON k.id = v.created_by_api_key_id
		LEFT JOIN accounts a ON a.id = k.account_id
		WHERE v.draft_id = ? ORDER BY v.version_number DESC`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var dirty int
		if err := rows.Scan(&v.ID, &v.DraftID, &v.VersionNumber, &v.ObjectKey, &v.ContentHash, &v.FileSize,
			&v.CreatedAt, &v.GitBranch, &v.GitCommitSHA, &v.GitCommitSubject, &dirty,
			&v.OriginalFilename, &v.CLIVersion, &v.Author); err != nil {
			return nil, err
		}
		v.GitDirty = dirty != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetStatus updates the review status.
func (d *DB) SetStatus(draftID, status string) error {
	_, err := d.sql.Exec(
		`UPDATE drafts SET status = ?, updated_at = datetime('now') WHERE id = ?`, status, draftID)
	return err
}

// SoftDelete marks a draft deleted.
func (d *DB) SoftDelete(draftID string) error {
	_, err := d.sql.Exec(
		`UPDATE drafts SET deleted_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`, draftID)
	return err
}

// SumStoredBytes is the total HTML bytes across live drafts (all versions).
// This powers the storage budget guard (default 5 GiB).
func (d *DB) SumStoredBytes() (int64, error) {
	var n int64
	err := d.sql.QueryRow(`
		SELECT COALESCE(SUM(v.file_size),0) FROM draft_versions v
		JOIN drafts dr ON dr.id = v.draft_id
		WHERE dr.deleted_at IS NULL`).Scan(&n)
	return n, err
}

// Stats is the control-panel overview.
type Stats struct {
	StoredBytes  int64 `json:"storedBytes"`
	DraftCount   int64 `json:"draftCount"`
	VersionCount int64 `json:"versionCount"`
	CommentCount int64 `json:"commentCount"`
	AccountCount int64 `json:"accountCount"`
	TeamCount    int64 `json:"teamCount"`
}

// Stats returns instance-wide totals (control panel / storage meter).
func (d *DB) Stats() (Stats, error) {
	var s Stats
	if err := d.sql.QueryRow(`SELECT COALESCE(SUM(v.file_size),0) FROM draft_versions v
		JOIN drafts dr ON dr.id = v.draft_id WHERE dr.deleted_at IS NULL`).Scan(&s.StoredBytes); err != nil {
		return s, err
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM drafts WHERE deleted_at IS NULL`).Scan(&s.DraftCount); err != nil {
		return s, err
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM draft_versions`).Scan(&s.VersionCount); err != nil {
		return s, err
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&s.CommentCount); err != nil {
		return s, err
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&s.AccountCount); err != nil {
		return s, err
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&s.TeamCount); err != nil {
		return s, err
	}
	return s, nil
}

// RevokeAPIKey revokes a key owned by accountID (no-op for other accounts).
func (d *DB) RevokeAPIKey(keyID, accountID string) error {
	_, err := d.sql.Exec(
		`UPDATE api_keys SET revoked_at = datetime('now') WHERE id = ? AND account_id = ?`,
		keyID, accountID)
	return err
}

// TeamMember is a member row joined with the account.
type TeamMember struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

// ListTeamMembers lists members of a team.
func (d *DB) ListTeamMembers(teamID string) ([]TeamMember, error) {
	rows, err := d.sql.Query(`
		SELECT tm.account_id, a.name, COALESCE(a.email,''), tm.role
		FROM team_members tm JOIN accounts a ON a.id = tm.account_id
		WHERE tm.team_id = ? ORDER BY tm.role DESC, a.name ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.AccountID, &m.Name, &m.Email, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RemoveTeamMember removes a member. The owner cannot be removed.
func (d *DB) RemoveTeamMember(teamID, accountID string) error {
	_, err := d.sql.Exec(
		`DELETE FROM team_members WHERE team_id = ? AND account_id = ? AND role != 'owner'`,
		teamID, accountID)
	return err
}

// ---- tags ---------------------------------------------------------------------------

// SetDraftTags replaces the tags on a draft (lowercased, deduped).
func (d *DB) SetDraftTags(draftID string, tags []string) error {
	if _, err := d.sql.Exec(`DELETE FROM draft_tags WHERE draft_id = ?`, draftID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		if _, err := d.sql.Exec(`INSERT OR IGNORE INTO draft_tags (draft_id, tag) VALUES (?, ?)`, draftID, t); err != nil {
			return err
		}
	}
	return nil
}

// DraftTags returns a draft's tags, sorted.
func (d *DB) DraftTags(draftID string) ([]string, error) {
	rows, err := d.sql.Query(`SELECT tag FROM draft_tags WHERE draft_id = ? ORDER BY tag`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TagsForDrafts returns a draftID → sorted tags map for the given drafts.
func (d *DB) TagsForDrafts(draftIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(draftIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(draftIDs)), ",")
	args := make([]any, 0, len(draftIDs))
	for _, id := range draftIDs {
		args = append(args, id)
	}
	rows, err := d.sql.Query(`SELECT draft_id, tag FROM draft_tags WHERE draft_id IN (`+placeholders+`) ORDER BY tag`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var did, tag string
		if err := rows.Scan(&did, &tag); err != nil {
			return nil, err
		}
		out[did] = append(out[did], tag)
	}
	return out, rows.Err()
}

// AllTagsForAccount returns every tag used across the account's drafts
// (including team drafts), for filter dropdowns.
func (d *DB) AllTagsForAccount(accountID string) ([]string, error) {
	rows, err := d.sql.Query(`
		SELECT DISTINCT t.tag FROM draft_tags t
		JOIN drafts dr ON dr.id = t.draft_id
		LEFT JOIN team_members tm ON tm.team_id = dr.team_id
		WHERE dr.deleted_at IS NULL AND (dr.account_id = ? OR tm.account_id = ?)
		ORDER BY t.tag`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// ---- comments ---------------------------------------------------------------------

// AddComment inserts a comment and returns it.
func (d *DB) AddComment(c Comment) (Comment, error) {
	c.ID = newID("cmt")
	_, err := d.sql.Exec(`
		INSERT INTO comments (id, draft_id, version_number, anchor, body, author)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.DraftID, c.VersionNumber, nullIfEmpty(c.Anchor), c.Body, c.Author)
	return c, err
}

// ListComments lists comments for a draft, oldest first.
func (d *DB) ListComments(draftID string) ([]Comment, error) {
	rows, err := d.sql.Query(`
		SELECT id, draft_id, version_number, COALESCE(anchor,''), body, author, created_at
		FROM comments WHERE draft_id = ? ORDER BY created_at ASC`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.DraftID, &c.VersionNumber, &c.Anchor, &c.Body, &c.Author, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- activity feed --------------------------------------------------------------------

// Activity is one feed entry: an upload, comment, status change, mention, or
// membership event. Rows are scoped to a personal account or a team.
type Activity struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	TeamID    string `json:"teamId"`
	DraftID   string `json:"draftId"`
	Kind      string `json:"kind"`
	Actor     string `json:"actor"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// AddActivity appends a feed entry.
func (d *DB) AddActivity(a Activity) error {
	a.ID = newID("act")
	_, err := d.sql.Exec(`
		INSERT INTO activity (id, account_id, team_id, draft_id, kind, actor, body)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, nullIfEmpty(a.AccountID), nullIfEmpty(a.TeamID), nullIfEmpty(a.DraftID),
		a.Kind, a.Actor, a.Body)
	return err
}

// ListActivity returns the account's feed: rows for its own drafts plus rows
// for teams it belongs to. Newest first.
func (d *DB) ListActivity(accountID string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.sql.Query(`
		SELECT a.id, COALESCE(a.account_id,''), COALESCE(a.team_id,''), COALESCE(a.draft_id,''),
		       a.kind, a.actor, a.body, a.created_at
		FROM activity a
		WHERE a.account_id = ?
		   OR a.team_id IN (SELECT team_id FROM team_members WHERE account_id = ?)
		ORDER BY a.created_at DESC, a.id DESC LIMIT ?`, accountID, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.AccountID, &a.TeamID, &a.DraftID, &a.Kind, &a.Actor, &a.Body, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UnreadActivity counts feed entries newer than the account's read marker.
func (d *DB) UnreadActivity(accountID string) (int64, error) {
	var n int64
	err := d.sql.QueryRow(`
		SELECT COUNT(*) FROM activity a
		WHERE (a.account_id = ? OR a.team_id IN (SELECT team_id FROM team_members WHERE account_id = ?))
		  AND a.created_at > COALESCE((SELECT feed_read_at FROM accounts WHERE id = ?), '')
		`, accountID, accountID, accountID).Scan(&n)
	return n, err
}

// MarkActivityRead sets the account's read marker to now.
func (d *DB) MarkActivityRead(accountID string) error {
	_, err := d.sql.Exec(`UPDATE accounts SET feed_read_at = datetime('now') WHERE id = ?`, accountID)
	return err
}

// ---- invites -------------------------------------------------------------------------

// Invite is a magic-link invitation to join a team.
type Invite struct {
	ID        string `json:"id"`
	TeamID    string `json:"teamId"`
	Email     string `json:"email"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	UsedAt    string `json:"usedAt"`
}

// CreateInvite stores a magic-link invite (7-day expiry) and returns the
// plaintext token (shown exactly once in the invite link).
func (d *DB) CreateInvite(teamID, email, createdBy string) (Invite, string, error) {
	token := randomToken(24)
	inv := Invite{
		ID:        newID("inv"),
		TeamID:    teamID,
		Email:     lowercase(email),
		CreatedBy: createdBy,
	}
	_, err := d.sql.Exec(`
		INSERT INTO invites (id, team_id, email, token_hash, created_by, expires_at)
		VALUES (?, ?, ?, ?, ?, datetime('now', '+7 days'))`,
		inv.ID, inv.TeamID, inv.Email, hashToken(token), createdBy)
	if err != nil {
		return Invite{}, "", err
	}
	found, err := d.FindInvite(inv.ID)
	if err != nil {
		return Invite{}, "", err
	}
	return *found, token, nil
}

// FindInvite returns an invite row by id.
func (d *DB) FindInvite(id string) (*Invite, error) {
	row := d.sql.QueryRow(`
		SELECT id, team_id, email, created_by, created_at, expires_at, COALESCE(used_at,'')
		FROM invites WHERE id = ?`, id)
	return scanInvite(row)
}

// FindInviteByToken resolves a magic-link token to its invite, or nil when
// unknown, used, or expired.
func (d *DB) FindInviteByToken(token string) (*Invite, error) {
	row := d.sql.QueryRow(`
		SELECT id, team_id, email, created_by, created_at, expires_at, COALESCE(used_at,'')
		FROM invites WHERE token_hash = ? AND used_at IS NULL AND expires_at > datetime('now')
		LIMIT 1`, hashToken(token))
	return scanInvite(row)
}

func scanInvite(row *sql.Row) (*Invite, error) {
	var inv Invite
	err := row.Scan(&inv.ID, &inv.TeamID, &inv.Email, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// ListInvites lists pending invites for a team (used ones included, flagged).
func (d *DB) ListInvites(teamID string) ([]Invite, error) {
	rows, err := d.sql.Query(`
		SELECT id, team_id, email, created_by, created_at, expires_at, COALESCE(used_at,'')
		FROM invites WHERE team_id = ? ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.TeamID, &inv.Email, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// UseInvite marks an invite as used.
func (d *DB) UseInvite(id string) error {
	_, err := d.sql.Exec(`UPDATE invites SET used_at = datetime('now') WHERE id = ?`, id)
	return err
}

// DeleteInvite revokes a pending invite.
func (d *DB) DeleteInvite(id string) error {
	_, err := d.sql.Exec(`DELETE FROM invites WHERE id = ? AND used_at IS NULL`, id)
	return err
}

// ---- webhooks ----------------------------------------------------------------------

// Webhook is an outbound notification endpoint (Discord/Slack/generic).
type Webhook struct {
	ID         string `json:"id"`
	AccountID  string `json:"accountId"`
	TeamID     string `json:"teamId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	Events     string `json:"events"` // comma-separated: upload,comment,status
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	LastStatus int64  `json:"lastStatus"`
	LastError  string `json:"lastError"`
}

// CreateWebhook inserts a webhook and returns it.
func (d *DB) CreateWebhook(w Webhook) (Webhook, error) {
	w.ID = newID("whk")
	if w.Kind == "" {
		w.Kind = "discord"
	}
	_, err := d.sql.Exec(`
		INSERT INTO webhooks (id, account_id, team_id, name, kind, url, events)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.AccountID, nullIfEmpty(w.TeamID), w.Name, w.Kind, w.URL, w.Events)
	if err != nil {
		return Webhook{}, err
	}
	found, err := d.FindWebhook(w.ID)
	if err != nil {
		return Webhook{}, err
	}
	if found == nil {
		return Webhook{}, fmt.Errorf("webhook insert failed")
	}
	return *found, nil
}

// FindWebhook returns a webhook by id.
func (d *DB) FindWebhook(id string) (*Webhook, error) {
	row := d.sql.QueryRow(`
		SELECT id, account_id, COALESCE(team_id,''), name, kind, url, events,
		       created_at, updated_at, COALESCE(last_status,0), COALESCE(last_error,'')
		FROM webhooks WHERE id = ?`, id)
	var w Webhook
	err := row.Scan(&w.ID, &w.AccountID, &w.TeamID, &w.Name, &w.Kind, &w.URL, &w.Events,
		&w.CreatedAt, &w.UpdatedAt, &w.LastStatus, &w.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWebhooks lists every webhook (server is single-instance; callers filter
// by ownership). Newest first.
func (d *DB) ListWebhooks() ([]Webhook, error) {
	rows, err := d.sql.Query(`
		SELECT id, account_id, COALESCE(team_id,''), name, kind, url, events,
		       created_at, updated_at, COALESCE(last_status,0), COALESCE(last_error,'')
		FROM webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.AccountID, &w.TeamID, &w.Name, &w.Kind, &w.URL, &w.Events,
			&w.CreatedAt, &w.UpdatedAt, &w.LastStatus, &w.LastError); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWebhook removes a webhook row.
func (d *DB) DeleteWebhook(id string) error {
	_, err := d.sql.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// SetWebhookResult records the outcome of the most recent delivery attempt.
func (d *DB) SetWebhookResult(id string, status int, errMsg string) error {
	_, err := d.sql.Exec(`
		UPDATE webhooks SET last_status = ?, last_error = ?, updated_at = datetime('now')
		WHERE id = ?`, status, nullIfEmpty(errMsg), id)
	return err
}

// ---- helpers ------------------------------------------------------------------------

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

var idAlphabet = []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// newID returns a prefixed random id (drafts keep the 12-char lowercase form).
func newID(prefix string) string {
	const draftLen = 12
	const internalLen = 20
	n := internalLen
	if prefix == "" {
		n = draftLen
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	if prefix == "" {
		for i := range b {
			if b[i] >= 'A' && b[i] <= 'Z' {
				b[i] = b[i] - 'A' + 'a'
			}
		}
		return string(b)
	}
	return prefix + "_" + string(b)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func lowercase(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func boolInt(b bool) any {
	if b {
		return 1
	}
	return 0
}

// now is a seam for tests.
func now() string { return time.Now().UTC().Format(time.RFC3339) }
