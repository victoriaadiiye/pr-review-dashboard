// Package store is the SQLite persistence layer for review events, PRs, and people.
package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db       *sql.DB
	excluded map[string]bool // logins hidden from leaderboard/history (bots, service accounts)
}

// SetExcludedLogins sets the denylist of logins to hide from the leaderboard,
// review history, and reviewer filter (bots and service accounts). Matching is
// case-insensitive. Safe to call once at startup before serving.
func (s *Store) SetExcludedLogins(logins []string) {
	m := make(map[string]bool, len(logins))
	for _, l := range logins {
		if l = strings.TrimSpace(l); l != "" {
			m[strings.ToLower(l)] = true
		}
	}
	s.excluded = m
}

// isExcluded reports whether a login is on the denylist (case-insensitive).
func (s *Store) isExcluded(login string) bool {
	return s.excluded[strings.ToLower(login)]
}

// ReviewEvent is one submitted review, already scored.
type ReviewEvent struct {
	Repo           string
	PRNumber       int
	Reviewer       string
	State          string
	InlineComments int
	BodyLen        int
	SubmittedAt    time.Time
	Points         int
	HasImage       bool
	RawHash        string
}

// PR is a pull request snapshot.
type PR struct {
	Repo               string
	Number             int
	Title              string
	Author             string
	URL                string
	IsDraft            bool
	ReadyAt            time.Time
	MergedAt           time.Time
	UpdatedAt          time.Time
	RequestedReviewers []string
	RequestedTeams     []string
	Additions          int
	Deletions          int
	ChangedFiles       int
	LastActivity       time.Time
	Reviewers          []QueueReviewer
	CommitsSinceReview int
}

// Person is a roster entry (team member or guest reviewer).
type Person struct {
	Login       string
	DisplayName string
	Team        string
	Active      bool
}

const schema = `
CREATE TABLE IF NOT EXISTS people (
  login TEXT PRIMARY KEY,
  display_name TEXT,
  team TEXT,
  active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS prs (
  repo TEXT, pr_number INTEGER,
  title TEXT, author TEXT, url TEXT,
  is_draft INTEGER, ready_at TEXT, merged_at TEXT, updated_at TEXT,
  requested_reviewers TEXT,
  additions INTEGER NOT NULL DEFAULT 0,
  deletions INTEGER NOT NULL DEFAULT 0,
  changed_files INTEGER NOT NULL DEFAULT 0,
  last_activity TEXT,
  reviewers_json TEXT,
  commits_since_review INTEGER NOT NULL DEFAULT 0,
  requested_teams TEXT,
  last_synced TEXT,
  PRIMARY KEY (repo, pr_number)
);
CREATE TABLE IF NOT EXISTS review_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT, pr_number INTEGER,
  reviewer TEXT, state TEXT,
  inline_comment_count INTEGER, body_len INTEGER,
  submitted_at TEXT, points INTEGER,
  has_image INTEGER NOT NULL DEFAULT 0,
  raw_hash TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_events_submitted ON review_events(submitted_at);
CREATE TABLE IF NOT EXISTS comment_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT, pr_number INTEGER,
  author TEXT, kind TEXT,
  body_len INTEGER, has_image INTEGER NOT NULL DEFAULT 0,
  created_at TEXT, points INTEGER,
  raw_hash TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_comment_created ON comment_events(created_at);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
`

// Open opens (or creates) the database at path and applies the schema.
// Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite is a single-writer file engine. Serializing the connection pool to
	// one connection prevents SQLITE_BUSY ("database is locked") when the poller
	// writes while the digest scheduler and HTTP API read concurrently.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func tsOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// UpsertReviewEvent inserts the event, or updates points/has_image if raw_hash exists.
func (s *Store) UpsertReviewEvent(e ReviewEvent) error {
	_, err := s.db.Exec(`
INSERT INTO review_events
  (repo, pr_number, reviewer, state, inline_comment_count, body_len, submitted_at, points, has_image, raw_hash)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(raw_hash) DO UPDATE SET points=excluded.points, has_image=excluded.has_image`,
		e.Repo, e.PRNumber, e.Reviewer, e.State, e.InlineComments, e.BodyLen,
		tsOrEmpty(e.SubmittedAt), e.Points, boolToInt(e.HasImage), e.RawHash)
	return err
}

// CommentEvent is one standalone PR comment, already scored.
type CommentEvent struct {
	Repo      string
	PRNumber  int
	Author    string
	Kind      string // "issue"
	BodyLen   int
	HasImage  bool
	CreatedAt time.Time
	Points    int
	RawHash   string
}

// UpsertCommentEvent inserts the comment, or updates points/has_image if raw_hash exists.
func (s *Store) UpsertCommentEvent(e CommentEvent) error {
	_, err := s.db.Exec(`
INSERT INTO comment_events
  (repo, pr_number, author, kind, body_len, has_image, created_at, points, raw_hash)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(raw_hash) DO UPDATE SET points=excluded.points, has_image=excluded.has_image`,
		e.Repo, e.PRNumber, e.Author, e.Kind, e.BodyLen, boolToInt(e.HasImage),
		tsOrEmpty(e.CreatedAt), e.Points, e.RawHash)
	return err
}

// GetMeta returns the value for key. found is false if the key is absent.
func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta inserts or updates the value for key.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// UpsertPR inserts or replaces a PR snapshot.
func (s *Store) UpsertPR(p PR) error {
	revJSON, err := json.Marshal(p.Reviewers)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO prs
  (repo, pr_number, title, author, url, is_draft, ready_at, merged_at, updated_at,
   requested_reviewers, additions, deletions, changed_files, last_activity, reviewers_json,
   commits_since_review, requested_teams, last_synced)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, pr_number) DO UPDATE SET
  title=excluded.title, author=excluded.author, url=excluded.url,
  is_draft=excluded.is_draft, ready_at=excluded.ready_at, merged_at=excluded.merged_at,
  updated_at=excluded.updated_at, requested_reviewers=excluded.requested_reviewers,
  additions=excluded.additions, deletions=excluded.deletions, changed_files=excluded.changed_files,
  last_activity=excluded.last_activity, reviewers_json=excluded.reviewers_json,
  commits_since_review=excluded.commits_since_review, requested_teams=excluded.requested_teams,
  last_synced=excluded.last_synced`,
		p.Repo, p.Number, p.Title, p.Author, p.URL, boolToInt(p.IsDraft),
		tsOrEmpty(p.ReadyAt), tsOrEmpty(p.MergedAt), tsOrEmpty(p.UpdatedAt),
		strings.Join(p.RequestedReviewers, ","), p.Additions, p.Deletions, p.ChangedFiles,
		tsOrEmpty(p.LastActivity), string(revJSON), p.CommitsSinceReview,
		strings.Join(p.RequestedTeams, ","), tsOrEmpty(time.Now()))
	return err
}

// RecordPRRef persists the identifying fields of a PR (title, url, author,
// merged_at) so history rows can resolve a title after merge. Unlike UpsertPR
// it only touches these columns on conflict, leaving queue-only columns
// (additions, reviewers_json, …) intact for PRs already snapshotted by the
// poller. Used by the merge-scan ingest path, whose PRs are otherwise absent
// from the prs table.
func (s *Store) RecordPRRef(repo string, number int, title, url, author string, mergedAt time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO prs (repo, pr_number, title, author, url, merged_at, last_synced)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(repo, pr_number) DO UPDATE SET
  title=excluded.title, author=excluded.author, url=excluded.url,
  merged_at=excluded.merged_at, last_synced=excluded.last_synced`,
		repo, number, title, author, url, tsOrEmpty(mergedAt), tsOrEmpty(time.Now()))
	return err
}

// MarkRepoPRsClosedExcept marks every still-"open" stored PR for repo whose
// number is NOT in openNumbers as no longer open, by stamping merged_at. The
// queue shows only merged_at=” rows, so this drops PRs that have merged or
// closed since they were snapshotted. The poller calls it each cycle with the
// authoritative open set fetched from GitHub.
//
// It is self-correcting: if a PR is in fact still open, the next poll re-upserts
// it with merged_at=” (UpsertPR), undoing a stamp from a transient empty fetch.
// Passing an empty openNumbers marks all of the repo's open rows closed.
func (s *Store) MarkRepoPRsClosedExcept(repo string, openNumbers []int, now time.Time) error {
	args := []any{tsOrEmpty(now), repo}
	q := `UPDATE prs SET merged_at = ? WHERE repo = ? AND merged_at = ''`
	if len(openNumbers) > 0 {
		ph := make([]string, len(openNumbers))
		for i, n := range openNumbers {
			ph[i] = "?"
			args = append(args, n)
		}
		q += ` AND pr_number NOT IN (` + strings.Join(ph, ",") + `)`
	}
	_, err := s.db.Exec(q, args...)
	return err
}

// UpsertPerson inserts or replaces a roster entry.
func (s *Store) UpsertPerson(p Person) error {
	_, err := s.db.Exec(`
INSERT INTO people (login, display_name, team, active)
VALUES (?,?,?,?)
ON CONFLICT(login) DO UPDATE SET
  display_name=excluded.display_name, team=excluded.team, active=excluded.active`,
		p.Login, p.DisplayName, p.Team, boolToInt(p.Active))
	return err
}

// EnsurePerson inserts a person only if their login does not already exist.
// Unlike UpsertPerson it never changes an existing row (so it cannot downgrade
// a roster member's team). Used to seed reviewers as guests on first sight.
func (s *Store) EnsurePerson(p Person) error {
	_, err := s.db.Exec(`
INSERT INTO people (login, display_name, team, active)
VALUES (?,?,?,?)
ON CONFLICT(login) DO NOTHING`,
		p.Login, p.DisplayName, p.Team, boolToInt(p.Active))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// migrate applies idempotent schema upgrades to a pre-existing database. New
// databases already have the columns from schema; this only adds what older
// ones lack. ADD COLUMN is guarded by a table_info check because SQLite has no
// ADD COLUMN IF NOT EXISTS.
func migrate(db *sql.DB) error {
	if !hasColumn(db, "review_events", "has_image") {
		if _, err := db.Exec(`ALTER TABLE review_events ADD COLUMN has_image INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, ddl string }{
		{"additions", "ALTER TABLE prs ADD COLUMN additions INTEGER NOT NULL DEFAULT 0"},
		{"deletions", "ALTER TABLE prs ADD COLUMN deletions INTEGER NOT NULL DEFAULT 0"},
		{"changed_files", "ALTER TABLE prs ADD COLUMN changed_files INTEGER NOT NULL DEFAULT 0"},
		{"last_activity", "ALTER TABLE prs ADD COLUMN last_activity TEXT"},
		{"reviewers_json", "ALTER TABLE prs ADD COLUMN reviewers_json TEXT"},
		{"commits_since_review", "ALTER TABLE prs ADD COLUMN commits_since_review INTEGER NOT NULL DEFAULT 0"},
		{"requested_teams", "ALTER TABLE prs ADD COLUMN requested_teams TEXT"},
	} {
		if !hasColumn(db, "prs", col.name) {
			if _, err := db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, col string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == col {
			return true
		}
	}
	return false
}
