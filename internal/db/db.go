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
	if err := d.ensureSentinel(); err != nil {
		return nil, err
	}
	return d, nil
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
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
	row := d.sql.QueryRow(`SELECT id, name, created_at FROM teams WHERE id = ?`, id)
	var t Team
	err := row.Scan(&t.ID, &t.Name, &t.CreatedAt)
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
		SELECT t.id, t.name, t.created_at FROM teams t
		JOIN team_members tm ON tm.team_id = t.id
		WHERE tm.account_id = ? ORDER BY t.created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
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
		SELECT id, draft_id, version_number, object_key, content_hash, file_size, created_at,
		       COALESCE(git_branch,''), COALESCE(git_commit_sha,''), COALESCE(git_commit_subject,''),
		       COALESCE(git_dirty,0), COALESCE(original_filename,''), COALESCE(cli_version,'')
		FROM draft_versions WHERE draft_id = ? ORDER BY version_number DESC`, draftID)
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
			&v.OriginalFilename, &v.CLIVersion); err != nil {
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
