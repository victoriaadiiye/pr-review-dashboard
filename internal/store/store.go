// Package store is the SQLite persistence layer for review events, PRs, and people.
package store

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
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
  last_synced TEXT,
  PRIMARY KEY (repo, pr_number)
);
CREATE TABLE IF NOT EXISTS review_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT, pr_number INTEGER,
  reviewer TEXT, state TEXT,
  inline_comment_count INTEGER, body_len INTEGER,
  submitted_at TEXT, points INTEGER,
  raw_hash TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_events_submitted ON review_events(submitted_at);
`

// Open opens (or creates) the database at path and applies the schema.
// Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
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

// UpsertReviewEvent inserts the event, or updates points if raw_hash already exists.
func (s *Store) UpsertReviewEvent(e ReviewEvent) error {
	_, err := s.db.Exec(`
INSERT INTO review_events
  (repo, pr_number, reviewer, state, inline_comment_count, body_len, submitted_at, points, raw_hash)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(raw_hash) DO UPDATE SET points=excluded.points`,
		e.Repo, e.PRNumber, e.Reviewer, e.State, e.InlineComments, e.BodyLen,
		tsOrEmpty(e.SubmittedAt), e.Points, e.RawHash)
	return err
}

// UpsertPR inserts or replaces a PR snapshot.
func (s *Store) UpsertPR(p PR) error {
	_, err := s.db.Exec(`
INSERT INTO prs
  (repo, pr_number, title, author, url, is_draft, ready_at, merged_at, updated_at, requested_reviewers, last_synced)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, pr_number) DO UPDATE SET
  title=excluded.title, author=excluded.author, url=excluded.url,
  is_draft=excluded.is_draft, ready_at=excluded.ready_at, merged_at=excluded.merged_at,
  updated_at=excluded.updated_at, requested_reviewers=excluded.requested_reviewers,
  last_synced=excluded.last_synced`,
		p.Repo, p.Number, p.Title, p.Author, p.URL, boolToInt(p.IsDraft),
		tsOrEmpty(p.ReadyAt), tsOrEmpty(p.MergedAt), tsOrEmpty(p.UpdatedAt),
		strings.Join(p.RequestedReviewers, ","), tsOrEmpty(time.Now()))
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
