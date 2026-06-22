# PR Review Leaderboard Dashboard — v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a working web dashboard that ranks the team on PR-review thoroughness (weekly/monthly/all-time) and shows the live ready-for-review queue, fed by a GitHub poller, in a single Go binary.

**Architecture:** One Go binary mirroring a single-binary reference service's deployment shape (Docker/Compose, `.env` + `projects.json`, Taskfile, launchd, `:8080` health) extended with an HTTP server that serves an embedded Vue dashboard + JSON API. A poller hits the GitHub GraphQL API every 15 min, a pure scorer assigns points, and an event-sourced SQLite store answers any time-window query.

**Tech Stack:** Go 1.25 (stdlib + `modernc.org/sqlite` pure-Go driver), GitHub GraphQL via `net/http`, Vue 3 + Vite (built assets embedded via `embed.FS`), Docker/Compose, go-task.

## Global Constraints

- Go **1.25** (matches single-binary reference service Dockerfile builder).
- **`CGO_ENABLED=0`** static build → SQLite driver MUST be pure-Go (`modernc.org/sqlite`). This is the only third-party runtime dep; justification: enables the static `debian-slim` image with no cgo toolchain.
- Stdlib `testing` only for Go tests — no external assertion/mocking libs (mirror single-binary reference service).
- All Go application code in small focused packages under `internal/`; `main.go` only wires them.
- GitHub auth via `GITHUB_TOKEN` (fallback `GH_TOKEN`) env var. Never hardcode tokens.
- Repos tracked come from `projects.json`: `acme/widgets`, `acme/gadgets`.
- Roster team: `acme/reviewers`. Non-roster reviewers are shown as `team='guest'`.
- Time-window boundaries use **Europe/Dublin**.
- Secrets (`.env`) are gitignored; never commit them.

---

## File Structure

```
pr-review-dashboard/
  go.mod  go.sum
  main.go                       # wiring only: load config, start poller + server
  internal/
    config/config.go            # env + projects.json loader
    scorer/scorer.go            # pure Score(review, weights) -> int
    store/store.go              # SQLite open + schema + upserts
    store/queries.go            # Leaderboard(window), Queue()
    github/client.go            # GraphQL client: FetchPullRequests, TeamMembers
    poller/poller.go            # github -> scorer -> store sync loop
    httpserver/server.go        # JSON API + embedded Vue assets
    httpserver/web/             # built Vue assets (go:embed target)
  web/                          # Vue source (Vite)
    package.json vite.config.ts index.html
    src/main.ts App.vue
    src/components/Leaderboard.vue Queue.vue
    src/__tests__/leaderboard.test.ts
  Dockerfile docker-compose.yml entrypoint.sh Taskfile.yaml
  projects.json .env.example
  com.youruser.pr-review-dashboard.plist
  slack-manifest.yaml           # already exists
```

---

### Task 1: Project skeleton + SQLite store

**Files:**
- Create: `go.mod`, `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type ReviewEvent struct { Repo string; PRNumber int; Reviewer, State string; InlineComments, BodyLen int; SubmittedAt time.Time; Points int; RawHash string }`
  - `type PR struct { Repo string; Number int; Title, Author, URL string; IsDraft bool; ReadyAt, MergedAt, UpdatedAt time.Time; RequestedReviewers []string }`
  - `type Person struct { Login, DisplayName, Team string; Active bool }`
  - `func Open(path string) (*Store, error)`
  - `func (s *Store) UpsertReviewEvent(e ReviewEvent) error`
  - `func (s *Store) UpsertPR(p PR) error`
  - `func (s *Store) UpsertPerson(p Person) error`
  - `func (s *Store) Close() error`

- [ ] **Step 1: Init module**

Run:
```bash
cd ~/projects/pr-review-dashboard
/usr/local/go/bin/go mod init pr-review-dashboard
/usr/local/go/bin/go get modernc.org/sqlite@latest
```
Expected: `go.mod` created with `modernc.org/sqlite` require line.

- [ ] **Step 2: Write the failing test** — `internal/store/store_test.go`

```go
package store

import (
	"testing"
	"time"
)

func TestUpsertReviewEventDedupesByRawHash(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	e := ReviewEvent{
		Repo: "acme/widgets", PRNumber: 1, Reviewer: "alice",
		State: "APPROVED", InlineComments: 0, BodyLen: 5,
		SubmittedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Points: 3, RawHash: "h1",
	}
	if err := s.UpsertReviewEvent(e); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same raw_hash, more points -> updates in place, no new row.
	e.Points = 9
	if err := s.UpsertReviewEvent(e); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count, pts int
	if err := s.db.QueryRow(`SELECT count(*), max(points) FROM review_events`).Scan(&count, &pts); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (dedupe)", count)
	}
	if pts != 9 {
		t.Errorf("points = %d, want 9 (updated)", pts)
	}
}

func TestUpsertPersonAndPR(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true}); err != nil {
		t.Fatalf("person: %v", err)
	}
	if err := s.UpsertPR(PR{Repo: "acme/widgets", Number: 7, Title: "x", Author: "bob", URL: "u", RequestedReviewers: []string{"alice"}}); err != nil {
		t.Fatalf("pr: %v", err)
	}
	var people, prs int
	s.db.QueryRow(`SELECT count(*) FROM people`).Scan(&people)
	s.db.QueryRow(`SELECT count(*) FROM prs`).Scan(&prs)
	if people != 1 || prs != 1 {
		t.Errorf("people=%d prs=%d, want 1/1", people, prs)
	}
}
```

- [ ] **Step 3: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/store/ -run TestUpsert -v`
Expected: FAIL — `undefined: Open` / package has no Go files.

- [ ] **Step 4: Implement** — `internal/store/store.go`

```go
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/store/ -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/store/
git commit -m "feat(store): SQLite schema + upserts with raw_hash dedupe"
```

---

### Task 2: Scorer (pure function)

**Files:**
- Create: `internal/scorer/scorer.go`
- Test: `internal/scorer/scorer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Weights struct { Base, Changes, Commented, Approved, PerInline, InlineCap, Substance, SubstanceChars int }`
  - `func Default() Weights`
  - `type Review struct { State string; InlineComments, BodyLen int; SelfReview bool }`
  - `func Score(r Review, w Weights) int`

- [ ] **Step 1: Write the failing test** — `internal/scorer/scorer_test.go`

```go
package scorer

import "testing"

func TestScore(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		r    Review
		want int
	}{
		{"bare approve", Review{State: "APPROVED", BodyLen: 3}, 3},                       // 2 base + 1 approved
		{"changes requested no comments", Review{State: "CHANGES_REQUESTED"}, 5},          // 2 + 3
		{"deep review", Review{State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400}, 13}, // 2+3+6+2
		{"inline cap", Review{State: "COMMENTED", InlineComments: 50, BodyLen: 0}, 14},     // 2+2+10(cap), no substance
		{"self review ignored", Review{State: "CHANGES_REQUESTED", SelfReview: true}, 0},
		{"substance threshold not met", Review{State: "APPROVED", BodyLen: 10}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(tt.r, w); got != tt.want {
				t.Errorf("Score(%+v) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/scorer/ -v`
Expected: FAIL — undefined `Default`/`Score`.

- [ ] **Step 3: Implement** — `internal/scorer/scorer.go`

```go
// Package scorer turns a submitted review into points. Pure, no I/O.
package scorer

// Weights configures the point values. See Default for the v1 baseline.
type Weights struct {
	Base           int
	Changes        int
	Commented      int
	Approved       int
	PerInline      int
	InlineCap      int
	Substance      int
	SubstanceChars int
}

// Default returns the v1 baseline weights from the spec.
func Default() Weights {
	return Weights{
		Base: 2, Changes: 3, Commented: 2, Approved: 1,
		PerInline: 1, InlineCap: 10, Substance: 2, SubstanceChars: 280,
	}
}

// Review is the scoreable shape of a submitted review.
type Review struct {
	State          string // APPROVED | CHANGES_REQUESTED | COMMENTED
	InlineComments int
	BodyLen        int // length of review body + inline comment text
	SelfReview     bool
}

// Score returns the points awarded for a review.
func Score(r Review, w Weights) int {
	if r.SelfReview {
		return 0
	}
	pts := w.Base
	switch r.State {
	case "CHANGES_REQUESTED":
		pts += w.Changes
	case "COMMENTED":
		pts += w.Commented
	case "APPROVED":
		pts += w.Approved
	}
	inline := r.InlineComments
	if inline > w.InlineCap {
		inline = w.InlineCap
	}
	pts += inline * w.PerInline
	if r.BodyLen > w.SubstanceChars {
		pts += w.Substance
	}
	return pts
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/scorer/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scorer/
git commit -m "feat(scorer): thoroughness-weighted review scoring"
```

---

### Task 3: Store queries (leaderboard + queue)

**Files:**
- Create: `internal/store/queries.go`
- Test: `internal/store/queries_test.go`

**Interfaces:**
- Consumes: `Store`, `ReviewEvent`, `PR`, `Person` from Task 1.
- Produces:
  - `type LeaderRow struct { Login, DisplayName, Team string; IsGuest bool; Points, Reviews int; AvgPoints float64; Rank int }`
  - `type QueueReviewer struct { Login, Status string }`
  - `type QueueRow struct { Repo string; PRNumber int; Title, Author, URL string; AgeHours float64; Reviewers []QueueReviewer }`
  - `func (s *Store) Leaderboard(window string, now time.Time) ([]LeaderRow, error)` — window ∈ `week|month|all`
  - `func (s *Store) Queue(now time.Time) ([]QueueRow, error)`
  - `func WindowStart(window string, now time.Time) time.Time`

- [ ] **Step 1: Write the failing test** — `internal/store/queries_test.go`

```go
package store

import (
	"testing"
	"time"
)

func seed(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Roster: alice (member), carol (member, no reviews -> zero row).
	s.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	s.UpsertPerson(Person{Login: "carol", DisplayName: "Carol", Team: "member", Active: true})
	// Guest reviewer dave appears via events only.
	s.UpsertPerson(Person{Login: "dave", DisplayName: "Dave", Team: "guest", Active: true})
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	mustEvent(t, s, "alice", "h1", 13, now.Add(-1*time.Hour))   // this week
	mustEvent(t, s, "alice", "h2", 3, now.Add(-10*24*time.Hour)) // last month-ish, not this week
	mustEvent(t, s, "dave", "h3", 5, now.Add(-2*time.Hour))      // this week
	return s
}

func mustEvent(t *testing.T, s *Store, who, hash string, pts int, at time.Time) {
	t.Helper()
	if err := s.UpsertReviewEvent(ReviewEvent{
		Repo: "acme/widgets", PRNumber: 1, Reviewer: who, State: "COMMENTED",
		Points: pts, RawHash: hash, SubmittedAt: at,
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
}

func TestLeaderboardWeekIncludesZerosAndGuests(t *testing.T) {
	s := seed(t)
	defer s.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	rows, err := s.Leaderboard("week", now)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	// alice 13, dave 5 (guest), carol 0. Ranked desc.
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].Login != "alice" || rows[0].Points != 13 || rows[0].Rank != 1 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].Login != "dave" || !rows[1].IsGuest {
		t.Errorf("row1 = %+v, want guest dave", rows[1])
	}
	if rows[2].Login != "carol" || rows[2].Points != 0 {
		t.Errorf("row2 = %+v, want carol 0", rows[2])
	}
}

func TestQueueDerivesReviewerStatus(t *testing.T) {
	s := seed(t)
	defer s.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	s.UpsertPR(PR{
		Repo: "acme/widgets", Number: 1, Title: "feat", Author: "bob", URL: "u",
		IsDraft: false, ReadyAt: now.Add(-5 * time.Hour),
		RequestedReviewers: []string{"alice", "carol"},
	})
	rows, err := s.Queue(now)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.AgeHours < 4.9 || r.AgeHours > 5.1 {
		t.Errorf("age = %v, want ~5h", r.AgeHours)
	}
	got := map[string]string{}
	for _, rv := range r.Reviewers {
		got[rv.Login] = rv.Status
	}
	if got["alice"] != "commented" {
		t.Errorf("alice status = %q, want commented", got["alice"])
	}
	if got["carol"] != "pending" {
		t.Errorf("carol status = %q, want pending", got["carol"])
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/store/ -run 'TestLeaderboard|TestQueue' -v`
Expected: FAIL — undefined `Leaderboard`/`Queue`/`LeaderRow`.

- [ ] **Step 3: Implement** — `internal/store/queries.go`

```go
package store

import (
	"sort"
	"strings"
	"time"
)

// LeaderRow is one ranked person on the leaderboard.
type LeaderRow struct {
	Login       string  `json:"login"`
	DisplayName string  `json:"display_name"`
	Team        string  `json:"team"`
	IsGuest     bool    `json:"is_guest"`
	Points      int     `json:"points"`
	Reviews     int     `json:"reviews"`
	AvgPoints   float64 `json:"avg_points_per_review"`
	Rank        int     `json:"rank"`
}

// QueueReviewer is a reviewer's status on a queued PR.
type QueueReviewer struct {
	Login  string `json:"login"`
	Status string `json:"status"` // approved | commented | changes | pending
}

// QueueRow is one PR awaiting review.
type QueueRow struct {
	Repo      string          `json:"repo"`
	PRNumber  int             `json:"pr_number"`
	Title     string          `json:"title"`
	Author    string          `json:"author"`
	URL       string          `json:"url"`
	AgeHours  float64         `json:"age_hours"`
	Reviewers []QueueReviewer `json:"reviewers"`
}

// WindowStart returns the inclusive lower bound for a leaderboard window.
// "all" returns the zero time. Boundaries are computed in Europe/Dublin.
func WindowStart(window string, now time.Time) time.Time {
	loc, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	switch window {
	case "week":
		// Monday 00:00.
		offset := (int(n.Weekday()) + 6) % 7 // Mon=0
		d := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
		return d
	case "month":
		return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	default: // all
		return time.Time{}
	}
}

// Leaderboard returns all active people (member roster + guests with activity),
// ranked by points within the window, descending. Zero-point roster members included.
func (s *Store) Leaderboard(window string, now time.Time) ([]LeaderRow, error) {
	start := WindowStart(window, now)
	rows, err := s.db.Query(`
SELECT p.login, p.display_name, p.team,
       COALESCE(SUM(CASE WHEN e.submitted_at >= ? OR ? = '' THEN e.points ELSE 0 END), 0) AS pts,
       COALESCE(SUM(CASE WHEN e.submitted_at >= ? OR ? = '' THEN 1 ELSE 0 END), 0) AS revs
FROM people p
LEFT JOIN review_events e ON e.reviewer = p.login
WHERE p.active = 1
GROUP BY p.login, p.display_name, p.team
HAVING p.team = 'member' OR pts > 0`,
		tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LeaderRow
	for rows.Next() {
		var r LeaderRow
		if err := rows.Scan(&r.Login, &r.DisplayName, &r.Team, &r.Points, &r.Reviews); err != nil {
			return nil, err
		}
		r.IsGuest = r.Team != "member"
		if r.Reviews > 0 {
			r.AvgPoints = float64(r.Points) / float64(r.Reviews)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		return out[i].Login < out[j].Login
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, rows.Err()
}

// Queue returns open, non-draft, unmerged PRs with per-requested-reviewer status,
// newest-ready first.
func (s *Store) Queue(now time.Time) ([]QueueRow, error) {
	rows, err := s.db.Query(`
SELECT repo, pr_number, title, author, url, ready_at, requested_reviewers
FROM prs
WHERE is_draft = 0 AND merged_at = ''
ORDER BY ready_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []QueueRow
	for rows.Next() {
		var q QueueRow
		var readyAt, reviewers string
		if err := rows.Scan(&q.Repo, &q.PRNumber, &q.Title, &q.Author, &q.URL, &readyAt, &reviewers); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, readyAt); err == nil {
			q.AgeHours = now.Sub(t).Hours()
		}
		for _, login := range splitNonEmpty(reviewers) {
			q.Reviewers = append(q.Reviewers, QueueReviewer{
				Login:  login,
				Status: s.reviewerStatus(q.Repo, q.PRNumber, login),
			})
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}

// reviewerStatus returns the latest review state for a reviewer on a PR, mapped
// to a display status. "pending" if they have not reviewed.
func (s *Store) reviewerStatus(repo string, pr int, login string) string {
	var state string
	err := s.db.QueryRow(`
SELECT state FROM review_events
WHERE repo = ? AND pr_number = ? AND reviewer = ?
ORDER BY submitted_at DESC LIMIT 1`, repo, pr, login).Scan(&state)
	if err != nil {
		return "pending"
	}
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes"
	case "COMMENTED":
		return "commented"
	default:
		return "pending"
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/store/ -v`
Expected: PASS (all store tests).

- [ ] **Step 5: Commit**

```bash
git add internal/store/queries.go internal/store/queries_test.go
git commit -m "feat(store): leaderboard window + ready-for-review queue queries"
```

---

### Task 4: GitHub GraphQL client

**Files:**
- Create: `internal/github/client.go`
- Test: `internal/github/client_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type FetchedReview struct { Author, State string; InlineComments, BodyLen int; SubmittedAt time.Time }`
  - `type FetchedPR struct { Number int; Title, Author, URL string; IsDraft bool; ReadyAt, MergedAt, UpdatedAt time.Time; RequestedReviewers []string; Reviews []FetchedReview }`
  - `type Client struct { ... }`
  - `func NewClient(token string) *Client`
  - `func (c *Client) WithEndpoint(url string) *Client` (test seam)
  - `func (c *Client) FetchPullRequests(ctx context.Context, owner, repo string) ([]FetchedPR, error)`
  - `func (c *Client) TeamMembers(ctx context.Context, org, team string) ([]string, error)`

- [ ] **Step 1: Write the failing test** — `internal/github/client_test.go`

Uses `httptest` to serve a canned GraphQL response; asserts parsing.

```go
package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const prResponse = `{"data":{"repository":{"pullRequests":{"nodes":[
{"number":1,"title":"feat","url":"u","isDraft":false,
 "author":{"login":"bob"},
 "createdAt":"2026-06-10T10:00:00Z","updatedAt":"2026-06-10T11:00:00Z","mergedAt":null,
 "reviewRequests":{"nodes":[{"requestedReviewer":{"login":"alice"}}]},
 "reviews":{"nodes":[
   {"author":{"login":"alice"},"state":"COMMENTED","submittedAt":"2026-06-10T10:30:00Z","body":"nice","comments":{"totalCount":2}}
 ]}}
],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`

func TestFetchPullRequestsParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(prResponse))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	prs, err := c.FetchPullRequests(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("prs = %d, want 1", len(prs))
	}
	p := prs[0]
	if p.Number != 1 || p.Author != "bob" || p.IsDraft {
		t.Errorf("pr = %+v", p)
	}
	if len(p.RequestedReviewers) != 1 || p.RequestedReviewers[0] != "alice" {
		t.Errorf("requested = %v", p.RequestedReviewers)
	}
	if len(p.Reviews) != 1 || p.Reviews[0].State != "COMMENTED" || p.Reviews[0].InlineComments != 2 {
		t.Errorf("review = %+v", p.Reviews)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/github/ -v`
Expected: FAIL — undefined `NewClient`.

- [ ] **Step 3: Implement** — `internal/github/client.go`

```go
// Package github is a minimal GitHub GraphQL client for PR + review + team data.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultEndpoint = "https://api.github.com/graphql"

// Client talks to the GitHub GraphQL API.
type Client struct {
	token      string
	endpoint   string
	httpClient *http.Client
}

// NewClient returns a client authenticating with the given token.
func NewClient(token string) *Client {
	return &Client{token: token, endpoint: defaultEndpoint, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

// WithEndpoint overrides the GraphQL endpoint (used in tests).
func (c *Client) WithEndpoint(url string) *Client { c.endpoint = url; return c }

// FetchedReview is a parsed review.
type FetchedReview struct {
	Author         string
	State          string
	InlineComments int
	BodyLen        int
	SubmittedAt    time.Time
}

// FetchedPR is a parsed pull request with its reviews.
type FetchedPR struct {
	Number             int
	Title              string
	Author             string
	URL                string
	IsDraft            bool
	ReadyAt            time.Time
	MergedAt           time.Time
	UpdatedAt          time.Time
	RequestedReviewers []string
	Reviews            []FetchedReview
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

const prQuery = `
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:OPEN,first:50,after:$cursor,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{
        number title url isDraft
        author{login}
        createdAt updatedAt mergedAt
        reviewRequests(first:20){nodes{requestedReviewer{... on User{login}}}}
        reviews(first:50){nodes{author{login} state submittedAt body comments{totalCount}}}
      }
      pageInfo{hasNextPage endCursor}
    }
  }
}`

type prGQL struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number    int    `json:"number"`
					Title     string `json:"title"`
					URL       string `json:"url"`
					IsDraft   bool   `json:"isDraft"`
					Author    *struct{ Login string } `json:"author"`
					CreatedAt time.Time  `json:"createdAt"`
					UpdatedAt time.Time  `json:"updatedAt"`
					MergedAt  *time.Time `json:"mergedAt"`
					ReviewRequests struct {
						Nodes []struct {
							RequestedReviewer *struct{ Login string } `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
					Reviews struct {
						Nodes []struct {
							Author      *struct{ Login string } `json:"author"`
							State       string     `json:"state"`
							SubmittedAt *time.Time `json:"submittedAt"`
							Body        string     `json:"body"`
							Comments    struct{ TotalCount int } `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchPullRequests returns all open PRs for owner/repo with their reviews.
func (c *Client) FetchPullRequests(ctx context.Context, owner, repo string) ([]FetchedPR, error) {
	var out []FetchedPR
	var cursor *string
	for {
		var resp prGQL
		vars := map[string]any{"owner": owner, "repo": repo, "cursor": cursor}
		if err := c.do(ctx, prQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Data.Repository.PullRequests.Nodes {
			p := FetchedPR{
				Number: n.Number, Title: n.Title, URL: n.URL, IsDraft: n.IsDraft,
				ReadyAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
			}
			p.Author = login(n.Author)
			if n.MergedAt != nil {
				p.MergedAt = *n.MergedAt
			}
			for _, rr := range n.ReviewRequests.Nodes {
				if rr.RequestedReviewer != nil {
					p.RequestedReviewers = append(p.RequestedReviewers, rr.RequestedReviewer.Login)
				}
			}
			for _, rv := range n.Reviews.Nodes {
				fr := FetchedReview{Author: login(rv.Author), State: rv.State, InlineComments: rv.Comments.TotalCount, BodyLen: len(rv.Body)}
				if rv.SubmittedAt != nil {
					fr.SubmittedAt = *rv.SubmittedAt
				}
				p.Reviews = append(p.Reviews, fr)
			}
			out = append(out, p)
		}
		pi := resp.Data.Repository.PullRequests.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}

func login(a *struct{ Login string }) string {
	if a == nil {
		return ""
	}
	return a.Login
}

const teamQuery = `
query($org:String!,$team:String!,$cursor:String){
  organization(login:$org){
    team(slug:$team){
      members(first:100,after:$cursor){
        nodes{login}
        pageInfo{hasNextPage endCursor}
      }
    }
  }
}`

type teamGQL struct {
	Data struct {
		Organization struct {
			Team struct {
				Members struct {
					Nodes    []struct{ Login string } `json:"nodes"`
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"members"`
			} `json:"team"`
		} `json:"organization"`
	} `json:"data"`
}

// TeamMembers returns the logins of every member of org/team.
func (c *Client) TeamMembers(ctx context.Context, org, team string) ([]string, error) {
	var out []string
	var cursor *string
	for {
		var resp teamGQL
		vars := map[string]any{"org": org, "team": team, "cursor": cursor}
		if err := c.do(ctx, teamQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data.Organization.Team.Members.Nodes {
			out = append(out, m.Login)
		}
		pi := resp.Data.Organization.Team.Members.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/github/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/github/
git commit -m "feat(github): GraphQL client for PRs, reviews, team members"
```

---

### Task 5: Poller (github → scorer → store)

**Files:**
- Create: `internal/poller/poller.go`
- Test: `internal/poller/poller_test.go`

**Interfaces:**
- Consumes: `github.FetchedPR`/`FetchedReview` (Task 4), `scorer.Score`/`Weights` (Task 2), `store.Store`/`PR`/`ReviewEvent`/`Person` (Tasks 1,3).
- Produces:
  - `type Source interface { FetchPullRequests(ctx, owner, repo string) ([]github.FetchedPR, error); TeamMembers(ctx, org, team string) ([]string, error) }`
  - `type Poller struct { ... }`
  - `func New(src Source, st *store.Store, w scorer.Weights) *Poller`
  - `func (p *Poller) SyncRepo(ctx context.Context, repo string) error` — repo as `owner/name`
  - `func (p *Poller) SyncRoster(ctx context.Context, team string) error` — team as `org/slug`
  - `func RawHash(repo string, pr int, reviewer, state string, at time.Time, inline int) string`

- [ ] **Step 1: Write the failing test** — `internal/poller/poller_test.go`

```go
package poller

import (
	"context"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

type fakeSource struct {
	prs     []github.FetchedPR
	members []string
}

func (f *fakeSource) FetchPullRequests(_ context.Context, _, _ string) ([]github.FetchedPR, error) {
	return f.prs, nil
}
func (f *fakeSource) TeamMembers(_ context.Context, _, _ string) ([]string, error) {
	return f.members, nil
}

func TestSyncRepoScoresAndStores(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	src := &fakeSource{prs: []github.FetchedPR{{
		Number: 1, Title: "feat", Author: "bob", URL: "u", IsDraft: false,
		ReadyAt:            time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		RequestedReviewers: []string{"alice"},
		Reviews: []github.FetchedReview{
			{Author: "alice", State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400,
				SubmittedAt: time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC)},
			{Author: "bob", State: "APPROVED", BodyLen: 1, // self-review -> 0 points, still stored
				SubmittedAt: time.Date(2026, 6, 10, 10, 35, 0, 0, time.UTC)},
		},
	}}}
	p := New(src, st, scorer.Default())
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	board, _ := st.Leaderboard("all", time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	// alice should have 13 points; bob self-review 0 (and bob not on roster/guest with 0 -> excluded).
	var alice *store.LeaderRow
	for i := range board {
		if board[i].Login == "alice" {
			alice = &board[i]
		}
	}
	if alice == nil || alice.Points != 13 {
		t.Fatalf("alice = %+v, want 13 points", alice)
	}
}

func TestSyncRosterMarksGuests(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	// Pre-existing event from a non-member reviewer.
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "dave", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})
	src := &fakeSource{members: []string{"alice", "carol"}}
	p := New(src, st, scorer.Default())
	if err := p.SyncRoster(context.Background(), "acme/reviewers"); err != nil {
		t.Fatalf("roster: %v", err)
	}
	board, _ := st.Leaderboard("all", time.Now())
	teams := map[string]string{}
	for _, r := range board {
		teams[r.Login] = r.Team
	}
	if teams["alice"] != "member" || teams["carol"] != "member" {
		t.Errorf("members not member: %v", teams)
	}
	if teams["dave"] != "guest" {
		t.Errorf("dave team = %q, want guest", teams["dave"])
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/poller/ -v`
Expected: FAIL — undefined `New`/`SyncRepo`.

- [ ] **Step 3: Implement** — `internal/poller/poller.go`

```go
// Package poller fetches GitHub data, scores reviews, and persists to the store.
package poller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

// Source is the subset of the GitHub client the poller needs (test seam).
type Source interface {
	FetchPullRequests(ctx context.Context, owner, repo string) ([]github.FetchedPR, error)
	TeamMembers(ctx context.Context, org, team string) ([]string, error)
}

// Poller syncs one or more repos and the roster into the store.
type Poller struct {
	src     Source
	st      *store.Store
	weights scorer.Weights
}

// New constructs a Poller.
func New(src Source, st *store.Store, w scorer.Weights) *Poller {
	return &Poller{src: src, st: st, weights: w}
}

// RawHash is the dedupe key for a review event.
func RawHash(repo string, pr int, reviewer, state string, at time.Time, inline int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, reviewer, state, at.UTC().Format(time.RFC3339), inline)))
	return fmt.Sprintf("%x", sum[:])
}

// SyncRepo fetches all open PRs for repo ("owner/name"), scores reviews, persists.
func (p *Poller) SyncRepo(ctx context.Context, repo string) error {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return fmt.Errorf("bad repo %q, want owner/name", repo)
	}
	prs, err := p.src.FetchPullRequests(ctx, owner, name)
	if err != nil {
		return err
	}
	for _, fp := range prs {
		if err := p.st.UpsertPR(store.PR{
			Repo: repo, Number: fp.Number, Title: fp.Title, Author: fp.Author, URL: fp.URL,
			IsDraft: fp.IsDraft, ReadyAt: fp.ReadyAt, MergedAt: fp.MergedAt, UpdatedAt: fp.UpdatedAt,
			RequestedReviewers: fp.RequestedReviewers,
		}); err != nil {
			return err
		}
		for _, rv := range fp.Reviews {
			pts := scorer.Score(scorer.Review{
				State: rv.State, InlineComments: rv.InlineComments, BodyLen: rv.BodyLen,
				SelfReview: rv.Author == fp.Author,
			}, p.weights)
			if err := p.st.UpsertReviewEvent(store.ReviewEvent{
				Repo: repo, PRNumber: fp.Number, Reviewer: rv.Author, State: rv.State,
				InlineComments: rv.InlineComments, BodyLen: rv.BodyLen,
				SubmittedAt: rv.SubmittedAt, Points: pts,
				RawHash: RawHash(repo, fp.Number, rv.Author, rv.State, rv.SubmittedAt, rv.InlineComments),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncRoster pulls team ("org/slug") members as member, and tags any other reviewer
// already seen in events as a guest.
func (p *Poller) SyncRoster(ctx context.Context, team string) error {
	org, slug, ok := splitRepo(team)
	if !ok {
		return fmt.Errorf("bad team %q, want org/slug", team)
	}
	members, err := p.src.TeamMembers(ctx, org, slug)
	if err != nil {
		return err
	}
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m] = true
		if err := p.st.UpsertPerson(store.Person{Login: m, DisplayName: m, Team: "member", Active: true}); err != nil {
			return err
		}
	}
	guests, err := p.st.DistinctReviewers()
	if err != nil {
		return err
	}
	for _, g := range guests {
		if memberSet[g] {
			continue
		}
		if err := p.st.UpsertPerson(store.Person{Login: g, DisplayName: g, Team: "guest", Active: true}); err != nil {
			return err
		}
	}
	return nil
}

func splitRepo(s string) (string, string, bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
```

- [ ] **Step 4: Add the supporting store method** — append to `internal/store/queries.go`

```go
// DistinctReviewers returns every login that has submitted a review event.
func (s *Store) DistinctReviewers() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT reviewer FROM review_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/poller/ ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/poller/ internal/store/queries.go
git commit -m "feat(poller): sync repos + roster, score reviews, tag guests"
```

---

### Task 6: HTTP server (JSON API + embed mount)

**Files:**
- Create: `internal/httpserver/server.go`, `internal/httpserver/web/.gitkeep`
- Test: `internal/httpserver/server_test.go`

**Interfaces:**
- Consumes: `store.Store` (Tasks 1,3).
- Produces:
  - `func New(st *store.Store, assets fs.FS) http.Handler`
  - Routes: `GET /api/leaderboard?window=`, `GET /api/queue`, `GET /health`, `GET /metrics`, `GET /` (assets).

- [ ] **Step 1: Write the failing test** — `internal/httpserver/server_test.go`

```go
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"pr-review-dashboard/internal/store"
)

func TestLeaderboardEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
	req := httptest.NewRequest(http.MethodGet, "/api/leaderboard?window=all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.LeaderRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Login != "alice" || rows[0].Points != 4 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestHealthEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health status = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/httpserver/ -v`
Expected: FAIL — undefined `New`.

- [ ] **Step 3: Implement** — `internal/httpserver/server.go`

```go
// Package httpserver exposes the JSON API and serves the embedded dashboard.
package httpserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"pr-review-dashboard/internal/store"
)

// New returns the HTTP handler. assets is the built Vue dashboard filesystem.
func New(st *store.Store, assets fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		window := r.URL.Query().Get("window")
		if window == "" {
			window = "week"
		}
		rows, err := st.Leaderboard(window, time.Now())
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.Queue(time.Now())
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().UTC()}, nil)
	})

	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if v == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/httpserver/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpserver/
git commit -m "feat(httpserver): JSON API + embedded asset mount"
```

---

### Task 7: Config loader

**Files:**
- Create: `internal/config/config.go`, `projects.json`, `.env.example`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { GitHubToken, RosterTeam, DBPath string; Repos []string; PollInterval time.Duration; HealthPort string; Weights scorer.Weights }`
  - `func Load(projectsPath string) (Config, error)` — reads env + projects.json.

- [ ] **Step 1: Write the failing test** — `internal/config/config_test.go`

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsEnvAndProjects(t *testing.T) {
	dir := t.TempDir()
	pj := filepath.Join(dir, "projects.json")
	os.WriteFile(pj, []byte(`{"projects":{"acme/widgets":{},"acme/gadgets":{}}}`), 0o644)

	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("ROSTER_TEAM", "acme/reviewers")
	t.Setenv("POLL_INTERVAL", "5m")

	c, err := Load(pj)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.GitHubToken != "tok" || c.RosterTeam != "acme/reviewers" {
		t.Errorf("config = %+v", c)
	}
	if len(c.Repos) != 2 {
		t.Errorf("repos = %v", c.Repos)
	}
	if c.PollInterval.Minutes() != 5 {
		t.Errorf("interval = %v", c.PollInterval)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `/usr/local/go/bin/go test ./internal/config/ -v`
Expected: FAIL — undefined `Load`.

- [ ] **Step 3: Implement** — `internal/config/config.go`

```go
// Package config loads runtime configuration from env vars and projects.json.
package config

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"pr-review-dashboard/internal/scorer"
)

// Config is the resolved runtime configuration.
type Config struct {
	GitHubToken  string
	RosterTeam   string
	DBPath       string
	Repos        []string
	PollInterval time.Duration
	HealthPort   string
	Weights      scorer.Weights
}

type projectsFile struct {
	Projects map[string]json.RawMessage `json:"projects"`
}

// Load resolves configuration. projectsPath points at projects.json.
func Load(projectsPath string) (Config, error) {
	c := Config{
		GitHubToken:  firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		RosterTeam:   envOr("ROSTER_TEAM", "acme/reviewers"),
		DBPath:       envOr("DB_PATH", "/data/leaderboard.db"),
		HealthPort:   envOr("HEALTH_PORT", "8080"),
		PollInterval: durationOr("POLL_INTERVAL", 15*time.Minute),
		Weights:      scorer.Default(),
	}
	b, err := os.ReadFile(projectsPath)
	if err != nil {
		return c, err
	}
	var pf projectsFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return c, err
	}
	for repo := range pf.Projects {
		c.Repos = append(c.Repos, repo)
	}
	sort.Strings(c.Repos)
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func durationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
```

- [ ] **Step 4: Create `projects.json`**

```json
{
  "projects": {
    "acme/widgets": {},
    "acme/gadgets": {}
  }
}
```

- [ ] **Step 5: Create `.env.example`**

```bash
# GitHub auth (dev: GITHUB_TOKEN=$(gh auth token))
GITHUB_TOKEN=
# Roster team whose members appear on the board
ROSTER_TEAM=acme/reviewers
# SQLite path (mounted volume in Docker)
DB_PATH=/data/leaderboard.db
# Poll interval
POLL_INTERVAL=15m
# Health/dashboard port
HEALTH_PORT=8080
# Slack (phase 2 — unused in v1)
SLACK_BOT_TOKEN=
DIGEST_CHANNEL_ID=
# Optional scoring overrides: PTS_BASE, PTS_CHANGES, PTS_COMMENTED, PTS_APPROVED,
# PTS_PER_INLINE, PTS_INLINE_CAP, PTS_SUBSTANCE, SUBSTANCE_CHARS
```

- [ ] **Step 6: Run tests, verify pass**

Run: `/usr/local/go/bin/go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ projects.json .env.example
git commit -m "feat(config): env + projects.json loader"
```

---

### Task 8: Vue dashboard

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/index.html`, `web/src/main.ts`, `web/src/App.vue`, `web/src/components/Leaderboard.vue`, `web/src/components/Queue.vue`, `web/src/__tests__/leaderboard.test.ts`

**Interfaces:**
- Consumes: `GET /api/leaderboard?window=`, `GET /api/queue` (Task 6).
- Produces: built static assets in `internal/httpserver/web/` (Vite `outDir`), embedded in Task 9.

- [ ] **Step 1: Scaffold Vite project files**

`web/package.json`:
```json
{
  "name": "pr-review-dashboard-web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "test": "vitest run"
  },
  "dependencies": { "vue": "^3.5.0" },
  "devDependencies": {
    "@vitejs/plugin-vue": "^5.1.0",
    "@vue/test-utils": "^2.4.6",
    "jsdom": "^25.0.0",
    "typescript": "^5.6.0",
    "vite": "^6.0.0",
    "vitest": "^2.1.0"
  }
}
```

`web/vite.config.ts` (build straight into the embed dir; proxy API in dev):
```ts
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: { outDir: '../internal/httpserver/web', emptyOutDir: true },
  server: { proxy: { '/api': 'http://localhost:8080' } },
  test: { environment: 'jsdom' },
})
```

`web/index.html`:
```html
<!doctype html>
<html>
  <head><meta charset="utf-8" /><title>PR Review Leaderboard</title></head>
  <body><div id="app"></div><script type="module" src="/src/main.ts"></script></body>
</html>
```

`web/src/main.ts`:
```ts
import { createApp } from 'vue'
import App from './App.vue'
createApp(App).mount('#app')
```

- [ ] **Step 2: Write the failing component test** — `web/src/__tests__/leaderboard.test.ts`

```ts
import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import Leaderboard from '../components/Leaderboard.vue'

describe('Leaderboard', () => {
  it('renders ranked rows and flags guests', () => {
    const rows = [
      { login: 'alice', display_name: 'Alice', team: 'member', is_guest: false, points: 13, reviews: 2, avg_points_per_review: 6.5, rank: 1 },
      { login: 'dave', display_name: 'Dave', team: 'guest', is_guest: true, points: 5, reviews: 1, avg_points_per_review: 5, rank: 2 },
    ]
    const wrapper = mount(Leaderboard, { props: { rows } })
    const text = wrapper.text()
    expect(text).toContain('Alice')
    expect(text).toContain('13')
    expect(wrapper.find('.guest').exists()).toBe(true)
  })
})
```

- [ ] **Step 3: Run test, verify it fails**

Run:
```bash
cd web && npm install && npm test
```
Expected: FAIL — cannot resolve `../components/Leaderboard.vue`.

- [ ] **Step 4: Implement components**

`web/src/components/Leaderboard.vue`:
```vue
<script setup lang="ts">
defineProps<{ rows: Array<{
  login: string; display_name: string; team: string; is_guest: boolean;
  points: number; reviews: number; avg_points_per_review: number; rank: number;
}> }>()
</script>

<template>
  <table class="leaderboard">
    <thead><tr><th>#</th><th>Reviewer</th><th>Points</th><th>Reviews</th><th>Avg</th></tr></thead>
    <tbody>
      <tr v-for="r in rows" :key="r.login" :class="{ guest: r.is_guest }">
        <td>{{ r.rank }}</td>
        <td>{{ r.display_name }}<span v-if="r.is_guest" class="guest-badge"> (guest)</span></td>
        <td>{{ r.points }}</td>
        <td>{{ r.reviews }}</td>
        <td>{{ r.avg_points_per_review.toFixed(1) }}</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.leaderboard { width: 100%; border-collapse: collapse; }
.leaderboard th, .leaderboard td { padding: 6px 10px; text-align: left; border-bottom: 1px solid #eee; }
.guest { opacity: 0.8; }
.guest-badge { color: #888; font-size: 0.85em; }
</style>
```

`web/src/components/Queue.vue`:
```vue
<script setup lang="ts">
defineProps<{ rows: Array<{
  repo: string; pr_number: number; title: string; author: string; url: string;
  age_hours: number; reviewers: Array<{ login: string; status: string }>;
}> }>()
const chip = (s: string) =>
  ({ approved: '✅', commented: '💬', changes: '🔴', pending: '⏳' } as Record<string, string>)[s] ?? '⏳'
</script>

<template>
  <ul class="queue">
    <li v-for="p in rows" :key="p.repo + p.pr_number">
      <a :href="p.url">{{ p.repo }}#{{ p.pr_number }}</a> — {{ p.title }}
      <span class="meta">by {{ p.author }}, {{ Math.round(p.age_hours) }}h old</span>
      <span class="reviewers">
        <span v-for="rv in p.reviewers" :key="rv.login">{{ chip(rv.status) }} {{ rv.login }}</span>
      </span>
    </li>
  </ul>
</template>

<style scoped>
.queue { list-style: none; padding: 0; }
.queue li { padding: 8px 0; border-bottom: 1px solid #eee; }
.meta { color: #888; font-size: 0.85em; margin-left: 6px; }
.reviewers { display: block; font-size: 0.9em; margin-top: 2px; }
.reviewers span { margin-right: 10px; }
</style>
```

`web/src/App.vue` (fetches both endpoints, window tabs):
```vue
<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import Leaderboard from './components/Leaderboard.vue'
import Queue from './components/Queue.vue'

const window = ref<'week' | 'month' | 'all'>('week')
const board = ref<any[]>([])
const queue = ref<any[]>([])

async function loadBoard() {
  board.value = await (await fetch(`/api/leaderboard?window=${window.value}`)).json()
}
async function loadQueue() {
  queue.value = await (await fetch('/api/queue')).json()
}
onMounted(() => { loadBoard(); loadQueue() })
watch(window, loadBoard)
</script>

<template>
  <main style="max-width: 880px; margin: 0 auto; font-family: system-ui;">
    <h1>🏆 PR Review Leaderboard</h1>
    <div class="tabs">
      <button v-for="w in ['week','month','all']" :key="w"
        :class="{ active: window === w }" @click="window = w as any">{{ w }}</button>
    </div>
    <Leaderboard :rows="board" />
    <h2>📋 Ready for review</h2>
    <Queue :rows="queue" />
  </main>
</template>

<style scoped>
.tabs button { margin-right: 6px; padding: 4px 12px; cursor: pointer; }
.tabs button.active { font-weight: bold; text-decoration: underline; }
</style>
```

- [ ] **Step 5: Run test, verify pass**

Run: `cd web && npm test`
Expected: PASS.

- [ ] **Step 6: Build assets into the embed dir**

Run: `cd web && npm run build`
Expected: files written to `internal/httpserver/web/` (index.html + assets).

- [ ] **Step 7: Commit**

```bash
git add web/ internal/httpserver/web/
git commit -m "feat(web): Vue leaderboard + ready-for-review queue dashboard"
```

---

### Task 9: Wiring (main.go) + deployment files

**Files:**
- Create: `main.go`, `Dockerfile`, `docker-compose.yml`, `entrypoint.sh`, `Taskfile.yaml`, `com.youruser.pr-review-dashboard.plist`
- Modify: `internal/httpserver/server.go` (add embed var in a sibling file)
- Create: `internal/httpserver/embed.go`

**Interfaces:**
- Consumes: all packages above.
- Produces: the running binary.

- [ ] **Step 1: Add the embed FS** — `internal/httpserver/embed.go`

```go
package httpserver

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var webFS embed.FS

// Assets returns the embedded built dashboard, rooted at the web/ dir.
func Assets() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // build-time guarantee: web/ is embedded
	}
	return sub
}
```

> Note: the `panic` here runs only at startup if the embed dir is missing (a build error), which is acceptable per the "bootstrap only" rule — it cannot fire once serving.

- [ ] **Step 2: Write `main.go`**

```go
// Command pr-review-dashboard polls GitHub for PR reviews, scores them, and
// serves a leaderboard + review-queue dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"pr-review-dashboard/internal/config"
	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/httpserver"
	"pr-review-dashboard/internal/poller"
	"pr-review-dashboard/internal/store"
)

func main() {
	cfg, err := config.Load("projects.json")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.GitHubToken == "" {
		log.Fatal("no GITHUB_TOKEN/GH_TOKEN set")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	p := poller.New(github.NewClient(cfg.GitHubToken), st, cfg.Weights)

	// Background sync loop.
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := p.SyncRoster(ctx, cfg.RosterTeam); err != nil {
				log.Printf("roster sync: %v", err)
			}
			for _, repo := range cfg.Repos {
				if err := p.SyncRepo(ctx, repo); err != nil {
					log.Printf("repo sync %s: %v", repo, err)
				}
			}
			cancel()
			time.Sleep(cfg.PollInterval)
		}
	}()

	h := httpserver.New(st, httpserver.Assets())
	addr := ":" + cfg.HealthPort
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		log.Fatalf("server: %v", err)
	}
}
```

- [ ] **Step 3: Verify the whole build + tests**

Run:
```bash
/usr/local/go/bin/go build ./...
/usr/local/go/bin/go test ./...
```
Expected: build succeeds; all Go tests PASS. (The `go:embed web` needs `internal/httpserver/web/` populated from Task 8 Step 6.)

- [ ] **Step 4: Create `Dockerfile`** (mirrors single-binary reference service; adds Node for the Vue build)

```dockerfile
FROM node:22-bookworm AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
COPY internal/httpserver/web /placeholder
RUN npm run build   # writes to ../internal/httpserver/web via vite outDir

FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/httpserver/web ./internal/httpserver/web
RUN CGO_ENABLED=0 go build -o /pr-review-dashboard .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /pr-review-dashboard /usr/local/bin/pr-review-dashboard
COPY projects.json /app/projects.json
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
WORKDIR /app
VOLUME ["/data"]
ENV HOME=/root
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD curl -f http://localhost:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
```

> Note: the Vue build (Task 8 Step 6) already commits `internal/httpserver/web/`, so the `web` stage is belt-and-suspenders. The `COPY --from=web` line keeps the image build reproducible without committed assets. If you prefer committed assets only, drop the `web` stage and the `COPY --from=web` line.

- [ ] **Step 5: Create `entrypoint.sh`** (mirrors single-binary reference service)

```sh
#!/bin/sh
GIT_TOKEN="${GH_TOKEN:-$GITHUB_TOKEN}"
if [ -n "$GIT_TOKEN" ]; then
    printf 'machine github.com\nlogin x-access-token\npassword %s\n' "$GIT_TOKEN" > /root/.netrc
    chmod 600 /root/.netrc
fi
exec pr-review-dashboard "$@"
```

- [ ] **Step 6: Create `docker-compose.yml`**

```yaml
services:
  dashboard:
    build: .
    env_file: .env
    volumes:
      - ${HOST_HOME:-/home/youruser}/.pr-review-dashboard:/data
    ports:
      - "${HEALTH_PORT:-8080}:8080"
    restart: unless-stopped
```

- [ ] **Step 7: Create `Taskfile.yaml`**

```yaml
version: "3"
vars:
  GO: /usr/local/go/bin/go
  BINARY: pr-review-dashboard
tasks:
  build:
    desc: Build the binary (after web assets exist)
    cmds:
      - "cd web && npm run build"
      - "{{.GO}} build -o {{.BINARY}} ."
  test:
    desc: Run all Go tests
    cmds:
      - "{{.GO}} test ./..."
  deploy:
    desc: Pull, build and start via Docker Compose
    cmds:
      - git pull
      - sudo docker compose up -d --build
  redeploy:
    desc: Rebuild from local code and restart
    cmds:
      - sudo docker compose up -d --build
  kill:
    desc: Stop the container
    cmds:
      - sudo docker compose down
  logs:
    desc: Tail logs
    cmds:
      - sudo docker compose logs -f
  status:
    desc: Container status + health
    cmds:
      - sudo docker compose ps
      - 'curl -s http://localhost:${HEALTH_PORT:-8080}/metrics || echo "not responding"'
```

- [ ] **Step 8: Create `com.youruser.pr-review-dashboard.plist`**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.youruser.pr-review-dashboard</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/youruser/projects/pr-review-dashboard/pr-review-dashboard</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/Users/youruser/projects/pr-review-dashboard</string>
    <key>KeepAlive</key><true/>
    <key>RunAtLoad</key><true/>
    <key>StandardOutPath</key>
    <string>/Users/youruser/projects/pr-review-dashboard/dashboard.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/youruser/projects/pr-review-dashboard/dashboard.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin</string>
        <key>DB_PATH</key>
        <string>/Users/youruser/projects/pr-review-dashboard/leaderboard.db</string>
    </dict>
</dict>
</plist>
```

- [ ] **Step 9: Smoke test locally**

Run:
```bash
GITHUB_TOKEN=$(gh auth token) DB_PATH=./leaderboard.db HEALTH_PORT=8080 ./pr-review-dashboard &
sleep 20
curl -s localhost:8080/health
curl -s 'localhost:8080/api/leaderboard?window=week' | head -c 400
curl -s localhost:8080/api/queue | head -c 400
kill %1
```
Expected: `ok`; JSON arrays (leaderboard shows member roster after first sync; queue lists open PRs).

- [ ] **Step 10: Commit**

```bash
git add main.go internal/httpserver/embed.go Dockerfile docker-compose.yml entrypoint.sh Taskfile.yaml com.youruser.pr-review-dashboard.plist
git commit -m "feat: wire poller+server in main, add Docker/Compose/Taskfile/plist deploy"
```

---

## Self-Review

**Spec coverage:**
- Repos acme/widgets+acme/gadgets → `projects.json` (Task 7) ✓
- Roster from member, guests → `SyncRoster` (Task 5), `Leaderboard` HAVING clause (Task 3) ✓
- Scoring formula + anti-gaming → scorer (Task 2), self-review handled in poller (Task 5) ✓
- Weekly/monthly/all-time → `WindowStart`/`Leaderboard` (Task 3) ✓
- Leaderboard panel + queue panel → Vue (Task 8), API (Task 6) ✓
- GraphQL ingestion, 15-min poll, token auth → github (Task 4), main loop (Task 9), config (Task 7) ✓
- Single Go binary, Docker/Compose/Taskfile/plist/healthcheck/SQLite volume → Task 9 ✓
- Stdlib-only tests, CGO_ENABLED=0, pure-Go sqlite → constraints honored ✓
- Slack digest → **explicitly out of v1 scope** (phase 2 plan); `.env` keys stubbed (Task 7) ✓

**Placeholder scan:** No TBD/TODO; every code step has complete code. The single `panic` in `embed.go` is documented as bootstrap-only.

**Type consistency:** `store.LeaderRow`/`QueueRow` JSON tags match the Vue prop shapes (Task 8) and API responses (Task 6). `poller.Source` matches `github.Client` method signatures (`FetchPullRequests`, `TeamMembers`). `store.DistinctReviewers` added in Task 5 Step 4 and consumed in `SyncRoster`.

## Out of scope (future plans)
- **Phase 2:** Slack digest scheduler (`chat.postMessage`, daily 09:00 Dublin, stale-PR list).
- **Phase 3:** peer 👍 helpful-review bonus (Slack reactions, manifest expansion, Socket Mode).
- Per-repo or per-language scoring multipliers; rank-delta history (current `rank_delta` is computed client-side or deferred).
