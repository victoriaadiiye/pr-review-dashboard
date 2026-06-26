# Scoring v2 + Merge-Driven Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Score PR review work at merge time (driven by a GitHub webhook) and reward thoroughness — written rationale and testing-proof screenshots outscore volume.

**Architecture:** A new `internal/webhook` package verifies the GitHub HMAC signature, and on a `closed`+`merged` `pull_request` event fetches that PR's reviews + issue comments via a new single-PR GraphQL query, scores each, and upserts into `review_events` / a new `comment_events` table. The poller drops to open-PR snapshotting + roster sync only (no scoring). The leaderboard unions review and comment points.

**Tech Stack:** Go 1.25 stdlib only (`net/http`, `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `encoding/json`), `modernc.org/sqlite` (already vendored). Tests: stdlib `testing` + `net/http/httptest`, hand-written fakes. No new dependencies.

## Global Constraints

- **No new third-party dependencies** — stdlib only.
- **Webhook is send-nothing, receive-only** — `POST /webhook/github`, HMAC-SHA256 verified.
- **Scoring happens at merge.** Reviews/comments on un-merged PRs score 0 until merge; PRs closed-without-merge never score. The poller no longer writes `review_events`.
- **Config keys (locked):** `WEBHOOK_SECRET` (none → route 503, disabled), `SCORE_IMAGE_BONUS` (5), `SCORE_MESSAGE_BUMP` (1), `SCORE_COMMENT_BASE` (1).
- **Message bump and Substance are mutually exclusive:** body empty → +0; 1–280 chars → +`MessageBump`; >280 chars → +`Substance`.
- **Image bonus gated on substance:** awarded only when `HasImage && BodyLen > SubstanceChars`.
- **Comment scoring scope (resolved VP4):** `comment_events` scores **issue comments only** (`kind = "issue"`). Lone inline comments already surface as `COMMENTED` reviews in GitHub's data model; a separate `inline` kind would double-count.
- **Idempotency:** every upsert dedupes on `raw_hash`; webhook redelivery is safe.
- **Errors:** never ignore an error; the webhook handler logs and maps to HTTP status. No `panic`.
- **Format:** `gofumpt`; exported types/funcs need doc comments.

### `has_image` detection (locked)

A body "has an image" if it contains any of: markdown image `![`…`](`…`)`; or the substring `user-images.githubusercontent.com`; or `github.com/user-attachments/`. Pure string check.

### Sample totals (the scorer must reproduce all of these)

| Shape | Points |
|---|---|
| bare approve, no message | 3 |
| approve + short message | 4 |
| comment-only review, short message | 5 |
| CHANGES_REQUESTED + short message | 6 |
| CHANGES_REQUESTED + long message, no image | 7 |
| approve + long message + screenshot | 10 |
| CHANGES_REQUESTED + long message + 5 inline | 12 |
| CHANGES_REQUESTED + long message + screenshot | 12 |
| CHANGES_REQUESTED + long message + 10 inline + screenshot | 22 |
| a general chat comment on a PR | 1 |
| a long comment with a testing screenshot | 6 |

---

## File Structure

- `internal/scorer/scorer.go` — extend `Weights`, `Review`; add `Comment`, `ScoreComment`, `HasImage`. (Task 1)
- `internal/store/store.go` — `has_image` column + `migrate`, `comment_events` table, `ReviewEvent.HasImage`, `CommentEvent` + `UpsertCommentEvent`. (Task 2)
- `internal/store/queries.go` — `Leaderboard` unions review + comment points. (Task 2)
- `internal/github/client.go` — `Body`/`ID` on `FetchedReview`; `FetchedComment`, `FetchedPRDetail`; `FetchPullRequest`. (Task 3)
- `internal/webhook/webhook.go` + `_test.go` — new package: signature verify, event filter, scoring pass. (Task 4)
- `internal/config/config.go` + `_test.go` — `WebhookSecret` + score-weight env keys. (Task 5)
- `internal/httpserver/server.go` + `_test.go` — mount `/webhook/github`. (Task 6)
- `internal/poller/poller.go` + `_test.go` — drop review scoring; `New` drops `weights`. (Task 7)
- `main.go` — wire webhook handler; poller without weights. (Task 7)
- `.env.example` — new keys. (Task 5)

---

## Task 1: Scorer — message bump, image bonus, `ScoreComment`, `HasImage`

**Files:**
- Modify: `internal/scorer/scorer.go`
- Test: `internal/scorer/scorer_test.go` (rewrite the table)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `Weights` gains `ImageBonus, MessageBump, CommentBase int`.
  - `Review` gains `HasImage bool`.
  - `func Score(r Review, w Weights) int` (extended).
  - `type Comment struct { BodyLen int; HasImage bool; SelfComment bool }`
  - `func ScoreComment(c Comment, w Weights) int`
  - `func HasImage(body string) bool`

- [ ] **Step 1: Rewrite the scorer test to the v2 sample totals**

Replace the entire contents of `internal/scorer/scorer_test.go`:

```go
package scorer

import "testing"

const longBody = 300 // > Default().SubstanceChars (280)

func TestScore(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		r    Review
		want int
	}{
		{"bare approve no message", Review{State: "APPROVED", BodyLen: 0}, 3},                                  // 2+1
		{"approve short message", Review{State: "APPROVED", BodyLen: 50}, 4},                                   // 2+1+1
		{"comment-only short message", Review{State: "COMMENTED", BodyLen: 50}, 5},                             // 2+2+1
		{"changes short message", Review{State: "CHANGES_REQUESTED", BodyLen: 50}, 6},                          // 2+3+1
		{"changes long no image", Review{State: "CHANGES_REQUESTED", BodyLen: longBody}, 7},                    // 2+3+2
		{"approve long + screenshot", Review{State: "APPROVED", BodyLen: longBody, HasImage: true}, 10},        // 2+1+2+5
		{"changes long + 5 inline", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, InlineComments: 5}, 12}, // 2+3+2+5
		{"changes long + screenshot", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, HasImage: true}, 12},  // 2+3+2+5
		{"changes long + 10 inline + screenshot", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, InlineComments: 10, HasImage: true}, 22}, // 2+3+2+10+5
		{"inline cap", Review{State: "COMMENTED", InlineComments: 50, BodyLen: 0}, 14},                         // 2+2+10(cap)
		{"self review ignored", Review{State: "CHANGES_REQUESTED", SelfReview: true}, 0},
		{"short body image gets no bonus", Review{State: "APPROVED", BodyLen: 50, HasImage: true}, 4},          // image gated on substance
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(tt.r, w); got != tt.want {
				t.Errorf("Score(%+v) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}

func TestScoreComment(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		c    Comment
		want int
	}{
		{"plain chat comment", Comment{BodyLen: 40}, 1},
		{"long comment + screenshot", Comment{BodyLen: longBody, HasImage: true}, 6}, // 1+5
		{"short comment + image no bonus", Comment{BodyLen: 40, HasImage: true}, 1},  // gated on substance
		{"self comment ignored", Comment{BodyLen: 40, SelfComment: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScoreComment(tt.c, w); got != tt.want {
				t.Errorf("ScoreComment(%+v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

func TestHasImage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"markdown image", "see ![shot](https://x/y.png) here", true},
		{"user-images host", "proof https://user-images.githubusercontent.com/1/2.png", true},
		{"user-attachments path", "https://github.com/user-attachments/assets/abc", true},
		{"plain text", "looks good to me, no screenshot", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasImage(tt.body); got != tt.want {
				t.Errorf("HasImage(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scorer/ -v`
Expected: FAIL — `unknown field HasImage in struct literal`, `undefined: ScoreComment`, `undefined: HasImage`.

- [ ] **Step 3: Write the implementation**

Replace the entire contents of `internal/scorer/scorer.go`:

```go
// Package scorer turns a submitted review or comment into points. Pure, no I/O.
package scorer

import "strings"

// Weights configures the point values. See Default for the v2 baseline.
type Weights struct {
	Base           int
	Changes        int
	Commented      int
	Approved       int
	PerInline      int
	InlineCap      int
	Substance      int
	SubstanceChars int
	ImageBonus     int // bonus for a testing-proof image, gated on substance
	MessageBump    int // bonus for a non-empty short body (<= SubstanceChars)
	CommentBase    int // flat points for a standalone comment
}

// Default returns the v2 baseline weights from the spec.
func Default() Weights {
	return Weights{
		Base: 2, Changes: 3, Commented: 2, Approved: 1,
		PerInline: 1, InlineCap: 10, Substance: 2, SubstanceChars: 280,
		ImageBonus: 5, MessageBump: 1, CommentBase: 1,
	}
}

// Review is the scoreable shape of a submitted review.
type Review struct {
	State          string // APPROVED | CHANGES_REQUESTED | COMMENTED
	InlineComments int
	BodyLen        int // length of the review body
	HasImage       bool
	SelfReview     bool
}

// Comment is the scoreable shape of a standalone PR comment.
type Comment struct {
	BodyLen     int
	HasImage    bool
	SelfComment bool
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
	pts += bodyAndImage(r.BodyLen, r.HasImage, w)
	return pts
}

// ScoreComment returns the points awarded for a standalone comment.
func ScoreComment(c Comment, w Weights) int {
	if c.SelfComment {
		return 0
	}
	pts := w.CommentBase
	if c.HasImage && c.BodyLen > w.SubstanceChars {
		pts += w.ImageBonus
	}
	return pts
}

// bodyAndImage adds the mutually-exclusive message-bump/substance points plus
// the substance-gated image bonus.
func bodyAndImage(bodyLen int, hasImage bool, w Weights) int {
	pts := 0
	switch {
	case bodyLen > w.SubstanceChars:
		pts += w.Substance
	case bodyLen > 0:
		pts += w.MessageBump
	}
	if hasImage && bodyLen > w.SubstanceChars {
		pts += w.ImageBonus
	}
	return pts
}

// HasImage reports whether body embeds an image or GitHub attachment.
func HasImage(body string) bool {
	if i := strings.Index(body, "!["); i >= 0 {
		if j := strings.Index(body[i:], "]("); j >= 0 && strings.Index(body[i+j:], ")") >= 0 {
			return true
		}
	}
	return strings.Contains(body, "user-images.githubusercontent.com") ||
		strings.Contains(body, "github.com/user-attachments/")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/scorer/ -v`
Expected: PASS — `TestScore`, `TestScoreComment`, `TestHasImage` all green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/scorer/scorer.go internal/scorer/scorer_test.go
git add internal/scorer/scorer.go internal/scorer/scorer_test.go
git commit -m "feat(scorer): message bump, image bonus, comment scoring, HasImage"
```

---

## Task 2: Store — `has_image` column + migration, `comment_events`, union leaderboard

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/queries.go`
- Test: `internal/store/queries_test.go` (append)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `ReviewEvent` gains `HasImage bool`.
  - `type CommentEvent struct { Repo string; PRNumber int; Author, Kind string; BodyLen int; HasImage bool; CreatedAt time.Time; Points int; RawHash string }`
  - `func (s *Store) UpsertCommentEvent(e CommentEvent) error`
  - `Leaderboard` Points = review points + comment points (windowed); `Reviews` counts `review_events` only.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/queries_test.go`:

```go
func TestLeaderboardUnionsCommentPoints(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	// One review worth 5, one comment worth 6 -> Points 11, Reviews 1.
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 5, RawHash: "rh1", SubmittedAt: now})
	if err := st.UpsertCommentEvent(store.CommentEvent{Repo: "r", PRNumber: 1, Author: "alice", Kind: "issue", BodyLen: 300, HasImage: true, Points: 6, RawHash: "ch1", CreatedAt: now}); err != nil {
		t.Fatalf("UpsertCommentEvent: %v", err)
	}
	// A guest who only left a comment must still appear.
	if err := st.UpsertCommentEvent(store.CommentEvent{Repo: "r", PRNumber: 2, Author: "dave", Kind: "issue", BodyLen: 10, Points: 1, RawHash: "ch2", CreatedAt: now}); err != nil {
		t.Fatalf("UpsertCommentEvent dave: %v", err)
	}
	st.EnsurePerson(store.Person{Login: "dave", DisplayName: "dave", Team: "guest", Active: true})

	board, err := st.Leaderboard("all", now)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	byLogin := map[string]store.LeaderRow{}
	for _, r := range board {
		byLogin[r.Login] = r
	}
	if a := byLogin["alice"]; a.Points != 11 || a.Reviews != 1 {
		t.Errorf("alice = %+v, want Points 11 Reviews 1", a)
	}
	if d, ok := byLogin["dave"]; !ok || d.Points != 1 {
		t.Errorf("dave = %+v (ok=%v), want Points 1", d, ok)
	}
}

func TestUpsertCommentEventDedupes(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	e := store.CommentEvent{Repo: "r", PRNumber: 1, Author: "alice", Kind: "issue", BodyLen: 10, Points: 1, RawHash: "same", CreatedAt: now}
	if err := st.UpsertCommentEvent(e); err != nil {
		t.Fatalf("first: %v", err)
	}
	e.Points = 6 // re-score on re-fetch
	if err := st.UpsertCommentEvent(e); err != nil {
		t.Fatalf("second: %v", err)
	}
	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", now)
	for _, r := range board {
		if r.Login == "alice" && r.Points != 6 {
			t.Errorf("alice points = %d, want 6 (deduped, re-scored)", r.Points)
		}
	}
}
```

> NOTE: `queries_test.go` already imports `store`, `testing`, `time`. Do not re-add imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run 'TestLeaderboardUnions|TestUpsertCommentEvent' -v`
Expected: FAIL — `undefined: store.CommentEvent` / `UpsertCommentEvent`.

- [ ] **Step 3: Implement store changes**

In `internal/store/store.go`:

(a) Add `"database/sql"` is already imported. Add the `HasImage` field to `ReviewEvent` (after `Points int`):

```go
	Points   int
	HasImage bool
	RawHash  string
```

(b) Add `comment_events` to the `schema` const (after the `review_events` index line, before the closing backtick):

```go
CREATE TABLE IF NOT EXISTS comment_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT, pr_number INTEGER,
  author TEXT, kind TEXT,
  body_len INTEGER, has_image INTEGER NOT NULL DEFAULT 0,
  created_at TEXT, points INTEGER,
  raw_hash TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_comment_created ON comment_events(created_at);
```

(c) In `Open`, call `migrate` after the schema `Exec` (replace the `if _, err := db.Exec(schema); ...` block's success path):

```go
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
```

(d) Add `migrate` + `hasColumn` at the bottom of the file:

```go
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
```

(e) Update `UpsertReviewEvent` to write `has_image`:

```go
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
```

(f) Add the `CommentEvent` type (after the `ReviewEvent` struct) and `UpsertCommentEvent` (after `UpsertReviewEvent`):

```go
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
```

- [ ] **Step 4: Rewrite the `Leaderboard` query to union comment points**

In `internal/store/queries.go`, replace the `s.db.Query(...)` call and its args inside `Leaderboard` with:

```go
	rows, err := s.db.Query(`
SELECT p.login, p.display_name, p.team,
       COALESCE(rv.pts, 0) + COALESCE(cm.pts, 0) AS pts,
       COALESCE(rv.revs, 0) AS revs
FROM people p
LEFT JOIN (
  SELECT reviewer, SUM(points) AS pts, COUNT(*) AS revs
  FROM review_events WHERE submitted_at >= ? OR ? = '' GROUP BY reviewer
) rv ON rv.reviewer = p.login
LEFT JOIN (
  SELECT author, SUM(points) AS pts
  FROM comment_events WHERE created_at >= ? OR ? = '' GROUP BY author
) cm ON cm.author = p.login
WHERE p.active = 1
  AND (p.team = 'member' OR (COALESCE(rv.pts, 0) + COALESCE(cm.pts, 0)) > 0)`,
		tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start))
```

The scan loop, guest derivation, avg, sort, and rank below it are unchanged.

- [ ] **Step 5: Run the full store suite**

Run: `go test ./internal/store/ -v`
Expected: PASS — new union/dedupe tests green; existing `store_test.go`/`queries_test.go` still green (the v1 `TestLeaderboard*` cases have no comment rows, so `COALESCE(cm.pts,0)=0` keeps their totals identical).

- [ ] **Step 6: Commit**

```bash
gofumpt -w internal/store/store.go internal/store/queries.go internal/store/queries_test.go
git add internal/store/store.go internal/store/queries.go internal/store/queries_test.go
git commit -m "feat(store): has_image column + migration, comment_events, union leaderboard"
```

---

## Task 3: GitHub client — single-PR fetch with reviews + issue comments

**Files:**
- Modify: `internal/github/client.go`
- Test: `internal/github/client_test.go` (append)

**Interfaces:**
- Consumes: existing `Client.do`.
- Produces:
  - `FetchedReview` gains `ID string` and `Body string`.
  - `type FetchedComment struct { ID, Author, Body string; CreatedAt time.Time }`
  - `type FetchedPRDetail struct { Number int; Author string; Reviews []FetchedReview; Comments []FetchedComment }`
  - `func (c *Client) FetchPullRequest(ctx context.Context, owner, repo string, number int) (FetchedPRDetail, error)`

- [ ] **Step 1: Write the failing test**

Append to `internal/github/client_test.go` (check the existing import block; ensure `context`, `net/http`, `net/http/httptest`, `testing` are present — add any missing):

```go
func TestFetchPullRequest(t *testing.T) {
	const body = `{"data":{"repository":{"pullRequest":{
		"number":42,
		"author":{"login":"carol"},
		"reviews":{"nodes":[
			{"id":"R1","author":{"login":"alice"},"state":"CHANGES_REQUESTED","submittedAt":"2026-06-20T10:00:00Z","body":"please fix","comments":{"totalCount":3}}
		]},
		"comments":{"nodes":[
			{"id":"C1","author":{"login":"bob"},"body":"nice work","createdAt":"2026-06-20T11:00:00Z"}
		]}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	d, err := c.FetchPullRequest(context.Background(), "acme", "widgets", 42)
	if err != nil {
		t.Fatalf("FetchPullRequest: %v", err)
	}
	if d.Number != 42 || d.Author != "carol" {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Reviews) != 1 || d.Reviews[0].Author != "alice" || d.Reviews[0].ID != "R1" ||
		d.Reviews[0].Body != "please fix" || d.Reviews[0].InlineComments != 3 {
		t.Errorf("reviews = %+v", d.Reviews)
	}
	if len(d.Comments) != 1 || d.Comments[0].Author != "bob" || d.Comments[0].ID != "C1" || d.Comments[0].Body != "nice work" {
		t.Errorf("comments = %+v", d.Comments)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/github/ -run TestFetchPullRequest -v`
Expected: FAIL — `undefined: (*Client).FetchPullRequest` / `d.Comments undefined`.

- [ ] **Step 3: Implement the single-PR fetch**

In `internal/github/client.go`:

(a) Add `ID` and `Body` to `FetchedReview`:

```go
// FetchedReview is a parsed review.
type FetchedReview struct {
	ID             string
	Author         string
	State          string
	Body           string
	InlineComments int
	BodyLen        int
	SubmittedAt    time.Time
}
```

(b) Add the new types and query + method (place after `FetchPullRequests` / its `login` helper):

```go
// FetchedComment is a parsed standalone PR issue comment.
type FetchedComment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
}

// FetchedPRDetail is one PR's full review + issue-comment history, for scoring
// at merge time.
type FetchedPRDetail struct {
	Number   int
	Author   string
	Reviews  []FetchedReview
	Comments []FetchedComment
}

const prDetailQuery = `
query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number
      author{login}
      reviews(first:100){nodes{id author{login} state submittedAt body comments{totalCount}}}
      comments(first:100){nodes{id author{login} body createdAt}}
    }
  }
}`

type prDetailGQL struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Number int                     `json:"number"`
				Author *struct{ Login string } `json:"author"`
				Reviews struct {
					Nodes []struct {
						ID          string                  `json:"id"`
						Author      *struct{ Login string } `json:"author"`
						State       string                  `json:"state"`
						SubmittedAt *time.Time              `json:"submittedAt"`
						Body        string                  `json:"body"`
						Comments    struct{ TotalCount int } `json:"comments"`
					} `json:"nodes"`
				} `json:"reviews"`
				Comments struct {
					Nodes []struct {
						ID        string                  `json:"id"`
						Author    *struct{ Login string } `json:"author"`
						Body      string                  `json:"body"`
						CreatedAt time.Time               `json:"createdAt"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchPullRequest returns one PR's full review + issue-comment history. Used by
// the merge webhook to score a single known PR rather than scanning open PRs.
func (c *Client) FetchPullRequest(ctx context.Context, owner, repo string, number int) (FetchedPRDetail, error) {
	var resp prDetailGQL
	vars := map[string]any{"owner": owner, "repo": repo, "number": number}
	if err := c.do(ctx, prDetailQuery, vars, &resp); err != nil {
		return FetchedPRDetail{}, err
	}
	pr := resp.Data.Repository.PullRequest
	d := FetchedPRDetail{Number: pr.Number, Author: login(pr.Author)}
	for _, rv := range pr.Reviews.Nodes {
		fr := FetchedReview{
			ID: rv.ID, Author: login(rv.Author), State: rv.State, Body: rv.Body,
			InlineComments: rv.Comments.TotalCount, BodyLen: len(rv.Body),
		}
		if rv.SubmittedAt != nil {
			fr.SubmittedAt = *rv.SubmittedAt
		}
		d.Reviews = append(d.Reviews, fr)
	}
	for _, cm := range pr.Comments.Nodes {
		d.Comments = append(d.Comments, FetchedComment{
			ID: cm.ID, Author: login(cm.Author), Body: cm.Body, CreatedAt: cm.CreatedAt,
		})
	}
	return d, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/github/ -v`
Expected: PASS — `TestFetchPullRequest` green; existing client tests still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/github/client.go internal/github/client_test.go
git add internal/github/client.go internal/github/client_test.go
git commit -m "feat(github): single-PR fetch with reviews + issue comments"
```

---

## Task 4: Webhook — signature verify, event filter, scoring pass

**Files:**
- Create: `internal/webhook/webhook.go`
- Test: `internal/webhook/webhook_test.go`

**Interfaces:**
- Consumes: `github.FetchedPRDetail`, `github.FetchedReview`, `github.FetchedComment`, `scorer.Score`, `scorer.ScoreComment`, `scorer.HasImage`, `scorer.Weights`, `store.UpsertReviewEvent`, `store.UpsertCommentEvent`, `store.EnsurePerson`.
- Produces:
  - `type PRFetcher interface { FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error) }`
  - `func New(secret string, fetcher PRFetcher, st *store.Store, w scorer.Weights) http.Handler`

- [ ] **Step 1: Write the failing test**

Create `internal/webhook/webhook_test.go`:

```go
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

type fakeFetcher struct {
	detail github.FetchedPRDetail
	calls  int
}

func (f *fakeFetcher) FetchPullRequest(_ context.Context, _, _ string, _ int) (github.FetchedPRDetail, error) {
	f.calls++
	return f.detail, nil
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, h http.Handler, event, sig string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(string(body)))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const mergedBody = `{"action":"closed","pull_request":{"number":42,"merged":true},"repository":{"full_name":"acme/widgets"}}`

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMergedEventScoresAndPersists(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 42, Author: "carol",
		Reviews: []github.FetchedReview{
			{ID: "R1", Author: "alice", State: "CHANGES_REQUESTED", Body: strings.Repeat("x", 300), InlineComments: 0},
		},
		Comments: []github.FetchedComment{
			{ID: "C1", Author: "bob", Body: "great"},
			{ID: "C2", Author: "carol", Body: "self comment ignored"}, // self -> 0, still stored
		},
	}}
	h := New("sekret", f, st, scorer.Default())

	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1", f.calls)
	}

	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	pts := map[string]int{}
	for _, r := range board {
		pts[r.Login] = r.Points
	}
	if pts["alice"] != 7 { // CHANGES(2+3) + substance(2)
		t.Errorf("alice points = %d, want 7", pts["alice"])
	}
	if pts["bob"] != 1 { // comment base
		t.Errorf("bob points = %d, want 1", pts["bob"])
	}
}

func TestBadSignatureRejected(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "sha256=deadbeef", body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMissingSignatureRejected(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDisabledWhenNoSecret(t *testing.T) {
	st := newStore(t)
	h := New("", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNonMergeEventIgnored(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{}
	h := New("sekret", f, st, scorer.Default())
	body := []byte(`{"action":"opened","pull_request":{"number":1,"merged":false},"repository":{"full_name":"acme/widgets"}}`)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if f.calls != 0 {
		t.Errorf("fetcher called %d times on non-merge, want 0", f.calls)
	}
}

func TestNonPREventIgnored(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(`{}`)
	rec := post(t, h, "push", sign("sekret", body), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
```

> NOTE: `timeNowUTC()` is a tiny test helper defined in the implementation file below as an exported-free package func? No — define it in the test file. Add at the bottom of `webhook_test.go`:
> ```go
> func timeNowUTC() time.Time { return time.Now().UTC() }
> ```
> and add `"time"` to the test import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/webhook/ -v`
Expected: FAIL — package has no non-test files / `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/webhook/webhook.go`:

```go
// Package webhook receives GitHub pull_request events and, on merge, scores the
// PR's reviews and comments into the store.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

// PRFetcher fetches a single PR's review + comment history (test seam).
type PRFetcher interface {
	FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error)
}

type handler struct {
	secret  string
	fetcher PRFetcher
	st      *store.Store
	weights scorer.Weights
}

// New returns the GitHub webhook handler. If secret is empty the route is
// disabled and returns 503, mirroring the digest-disabled pattern.
func New(secret string, fetcher PRFetcher, st *store.Store, w scorer.Weights) http.Handler {
	return &handler{secret: secret, fetcher: fetcher, st: st, weights: w}
}

type prEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int  `json:"number"`
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.secret == "" {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if !verifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var ev prEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if ev.Action != "closed" || !ev.PullRequest.Merged {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	owner, repo, ok := splitFullName(ev.Repository.FullName)
	if !ok {
		http.Error(w, "bad repository.full_name", http.StatusBadRequest)
		return
	}
	if err := h.score(r.Context(), ev.Repository.FullName, owner, repo, ev.PullRequest.Number); err != nil {
		log.Printf("webhook score %s#%d: %v", ev.Repository.FullName, ev.PullRequest.Number, err)
		http.Error(w, "scoring failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("scored"))
}

// score fetches the PR and upserts scored reviews + issue comments. fullName is
// the "owner/repo" string stored on each event row.
func (h *handler) score(ctx context.Context, fullName, owner, repo string, number int) error {
	d, err := h.fetcher.FetchPullRequest(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, rv := range d.Reviews {
		hasImg := scorer.HasImage(rv.Body)
		pts := scorer.Score(scorer.Review{
			State: rv.State, InlineComments: rv.InlineComments, BodyLen: len(rv.Body),
			HasImage: hasImg, SelfReview: rv.Author == d.Author,
		}, h.weights)
		if err := h.st.UpsertReviewEvent(store.ReviewEvent{
			Repo: fullName, PRNumber: number, Reviewer: rv.Author, State: rv.State,
			InlineComments: rv.InlineComments, BodyLen: len(rv.Body), SubmittedAt: rv.SubmittedAt,
			Points: pts, HasImage: hasImg,
			RawHash: hashKey(fullName, number, rv.Author, "review", rv.ID, len(rv.Body)),
		}); err != nil {
			return fmt.Errorf("upsert review %s: %w", rv.ID, err)
		}
		if err := h.seedGuest(rv.Author); err != nil {
			return err
		}
	}
	for _, cm := range d.Comments {
		hasImg := scorer.HasImage(cm.Body)
		pts := scorer.ScoreComment(scorer.Comment{
			BodyLen: len(cm.Body), HasImage: hasImg, SelfComment: cm.Author == d.Author,
		}, h.weights)
		if err := h.st.UpsertCommentEvent(store.CommentEvent{
			Repo: fullName, PRNumber: number, Author: cm.Author, Kind: "issue",
			BodyLen: len(cm.Body), HasImage: hasImg, CreatedAt: cm.CreatedAt, Points: pts,
			RawHash: hashKey(fullName, number, cm.Author, "issue", cm.ID, len(cm.Body)),
		}); err != nil {
			return fmt.Errorf("upsert comment %s: %w", cm.ID, err)
		}
		if err := h.seedGuest(cm.Author); err != nil {
			return err
		}
	}
	return nil
}

// seedGuest records an actor as a guest if not already on the roster. Never
// downgrades an existing member (EnsurePerson is insert-if-absent).
func (h *handler) seedGuest(login string) error {
	if login == "" {
		return nil
	}
	return h.st.EnsurePerson(store.Person{Login: login, DisplayName: login, Team: "guest", Active: true})
}

func verifySignature(secret string, body []byte, header string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header))
}

func splitFullName(s string) (string, string, bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// hashKey is the idempotency key for an event row. Including bodyLen means an
// edited body re-scores; the stable node id prevents double-counting redelivery.
func hashKey(repo string, pr int, actor, kind, id string, bodyLen int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, actor, kind, id, bodyLen)))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/webhook/ -v`
Expected: PASS — all six webhook tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/webhook/webhook.go internal/webhook/webhook_test.go
git add internal/webhook/webhook.go internal/webhook/webhook_test.go
git commit -m "feat(webhook): merge-driven GitHub webhook with signature verify + scoring pass"
```

---

## Task 5: Config — `WEBHOOK_SECRET` + score-weight env keys

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go` (append)
- Modify: `.env.example`

**Interfaces:**
- Produces (added to `config.Config`): `WebhookSecret string`. `Weights` is loaded from `scorer.Default()` then overridden by `SCORE_IMAGE_BONUS`, `SCORE_MESSAGE_BUMP`, `SCORE_COMMENT_BASE`.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadWebhookAndScoreConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("WEBHOOK_SECRET", "sekret")
	t.Setenv("SCORE_IMAGE_BONUS", "9")
	t.Setenv("SCORE_MESSAGE_BUMP", "2")
	t.Setenv("SCORE_COMMENT_BASE", "3")

	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WebhookSecret != "sekret" {
		t.Errorf("WebhookSecret = %q", c.WebhookSecret)
	}
	if c.Weights.ImageBonus != 9 || c.Weights.MessageBump != 2 || c.Weights.CommentBase != 3 {
		t.Errorf("weights = %+v", c.Weights)
	}
}

func TestLoadScoreWeightDefaults(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("SCORE_IMAGE_BONUS", "")
	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Weights.ImageBonus != 5 || c.Weights.MessageBump != 1 || c.Weights.CommentBase != 1 {
		t.Errorf("default weights = %+v, want 5/1/1", c.Weights)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestLoadWebhook|TestLoadScoreWeight' -v`
Expected: FAIL — `c.WebhookSecret undefined`.

- [ ] **Step 3: Implement config changes**

In `internal/config/config.go`:

(a) Add the field to `Config` (after `StalePRHours float64`):

```go
	WebhookSecret   string
```

(b) In the `c := Config{...}` literal, after `Weights: scorer.Default(),`, the weights default is set; override below the literal. Add `WebhookSecret` to the literal:

```go
		WebhookSecret:   os.Getenv("WEBHOOK_SECRET"),
```

(c) After the `c := Config{...}` literal closes (before the `if repos := ...` block), override the weight fields:

```go
	c.Weights.ImageBonus = intOr("SCORE_IMAGE_BONUS", c.Weights.ImageBonus)
	c.Weights.MessageBump = intOr("SCORE_MESSAGE_BUMP", c.Weights.MessageBump)
	c.Weights.CommentBase = intOr("SCORE_COMMENT_BASE", c.Weights.CommentBase)
```

(d) Add the `intOr` helper at the bottom (next to `floatOr`):

```go
func intOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all config tests green.

- [ ] **Step 5: Update `.env.example`**

Append:

```
# GitHub webhook: HMAC secret for POST /webhook/github. Empty => webhook disabled (503).
WEBHOOK_SECRET=
# Scoring weights (optional overrides)
SCORE_IMAGE_BONUS=5
SCORE_MESSAGE_BUMP=1
SCORE_COMMENT_BASE=1
```

- [ ] **Step 6: Commit**

```bash
gofumpt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go .env.example
git commit -m "feat(config): WEBHOOK_SECRET + score-weight overrides"
```

---

## Task 6: HTTP server — mount `/webhook/github`

**Files:**
- Modify: `internal/httpserver/server.go`
- Modify: `internal/httpserver/server_test.go`

**Interfaces:**
- `New` signature becomes `New(st *store.Store, assets fs.FS, runDigest func(context.Context) error, webhook http.Handler) http.Handler`. `webhook` may be `nil` → route returns `503`.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpserver/server_test.go`:

```go
func TestWebhookRouteMounted(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	hook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("scored"))
	})
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, hook)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "scored" {
		t.Errorf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestWebhookRouteDisabled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
```

Then update the **five** existing `New(...)` call sites in this file to pass a fourth arg `nil`:
- `TestLeaderboardEndpoint` (line ~22): `New(st, fstest.MapFS{...}, nil, nil)`
- `TestHealthEndpoint` (line ~42): `New(st, fstest.MapFS{}, nil, nil)`
- `TestDigestRunTrigger` (line ~60): `New(st, fstest.MapFS{...}, run, nil)`
- `TestDigestRunDisabled` (line ~77): `New(st, fstest.MapFS{...}, nil, nil)`
- `TestDigestRunRejectsGET` (line ~90): `New(st, fstest.MapFS{...}, func(_ context.Context) error { return nil }, nil)`

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpserver/ -v`
Expected: FAIL — `not enough arguments in call to New`.

- [ ] **Step 3: Implement the route**

In `internal/httpserver/server.go`, change the signature and add the route (after the `/digest/run` block, before `/api/leaderboard`):

```go
func New(st *store.Store, assets fs.FS, runDigest func(context.Context) error, webhook http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/webhook/github", func(w http.ResponseWriter, r *http.Request) {
		if webhook == nil {
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}
		webhook.ServeHTTP(w, r)
	})
```

(The existing `/digest/run`, `/api/*`, `/health`, `/metrics`, `/` registrations stay; just place the webhook block right after `mux := http.NewServeMux()`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpserver/ -v`
Expected: PASS — new webhook route tests + existing tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/httpserver/server.go internal/httpserver/server_test.go
git add internal/httpserver/server.go internal/httpserver/server_test.go
git commit -m "feat(httpserver): mount POST /webhook/github"
```

---

## Task 7: Poller drops scoring + wire webhook into `main.go`

**Files:**
- Modify: `internal/poller/poller.go`
- Modify: `internal/poller/poller_test.go`
- Modify: `main.go`

**Interfaces:**
- `poller.New` signature becomes `New(src Source, st *store.Store) *Poller` (drops `weights`).
- `SyncRepo` snapshots PRs only — no `review_events`, no scoring, no guest seeding from reviews.
- `main.go` builds the webhook handler when `cfg.WebhookSecret != ""` and passes it (and `nil` otherwise) into `httpserver.New`.

- [ ] **Step 1: Rewrite the poller test**

Replace the entire contents of `internal/poller/poller_test.go`:

```go
package poller

import (
	"context"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
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

// SyncRepo snapshots PRs for the queue but must NOT score reviews in v2 —
// scoring moved to the merge webhook.
func TestSyncRepoSnapshotsButDoesNotScore(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	src := &fakeSource{prs: []github.FetchedPR{{
		Number: 1, Title: "feat", Author: "bob", URL: "u", IsDraft: false,
		ReadyAt:            time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		RequestedReviewers: []string{"alice"},
		Reviews: []github.FetchedReview{
			{Author: "alice", State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400,
				SubmittedAt: time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC)},
		},
	}}}
	p := New(src, st)
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The PR is snapshotted into the queue.
	q, err := st.Queue(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(q) != 1 || q[0].PRNumber != 1 {
		t.Fatalf("queue = %+v, want 1 PR #1", q)
	}

	// No review_events were written, so DistinctReviewers is empty.
	revs, err := st.DistinctReviewers()
	if err != nil {
		t.Fatalf("DistinctReviewers: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("DistinctReviewers = %v, want none (poller must not score)", revs)
	}
}

func TestSyncRosterMarksGuests(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	// Pre-existing event from a non-member reviewer (as the webhook would write).
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "dave", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})
	src := &fakeSource{members: []string{"alice", "carol"}}
	p := New(src, st)
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

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/poller/ -v`
Expected: FAIL — `not enough arguments in call to New` (still takes `weights`) and the scoring loop still writes events.

- [ ] **Step 3: Strip scoring from the poller**

In `internal/poller/poller.go`:

(a) Remove the `scorer` import and the `weights` field. The struct + constructor become:

```go
// Poller syncs one or more repos and the roster into the store. In v2 it only
// snapshots open PRs for the queue and syncs the roster; scoring happens in the
// merge webhook.
type Poller struct {
	src Source
	st  *store.Store
}

// New constructs a Poller.
func New(src Source, st *store.Store) *Poller {
	return &Poller{src: src, st: st}
}
```

(b) Delete the `RawHash` function (moved to the webhook package as `hashKey`).

(c) Replace `SyncRepo`'s body so it only upserts the PR snapshot (drop the inner review loop, scoring, and `EnsurePerson`):

```go
// SyncRepo fetches open PRs for repo ("owner/name") and snapshots them for the
// queue. It does not score reviews — that is the merge webhook's job.
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
	}
	return nil
}
```

(d) Remove now-unused imports: `crypto/sha256` and `time` are only used by `RawHash` — delete them if no longer referenced. (`SyncRepo`/`SyncRoster`/`splitRepo` use only `context`, `fmt`, `strings`, plus the internal `github`/`store` packages.) Run `goimports`/`gofumpt` will not remove unused imports — delete them by hand or `go build` will report them.

- [ ] **Step 4: Wire the webhook into `main.go`**

In `main.go`:

(a) Add the import `"pr-review-dashboard/internal/webhook"`.

(b) Change the poller construction (drop `cfg.Weights`):

```go
	p := poller.New(github.NewClient(cfg.GitHubToken), st)
```

(c) After the digest block and before `h := httpserver.New(...)`, build the webhook handler:

```go
	// GitHub merge webhook: enabled only when a secret is configured.
	var webhookHandler http.Handler
	if cfg.WebhookSecret != "" {
		webhookHandler = webhook.New(cfg.WebhookSecret, github.NewClient(cfg.GitHubToken), st, cfg.Weights)
		log.Print("webhook enabled at POST /webhook/github")
	} else {
		log.Print("webhook disabled: set WEBHOOK_SECRET to enable")
	}

	h := httpserver.New(st, httpserver.Assets(), runDigest, webhookHandler)
```

Remove the old `h := httpserver.New(st, httpserver.Assets(), runDigest)` line.

- [ ] **Step 5: Build the whole binary**

Run: `go build ./...`
Expected: success. If it reports `"github" imported and not used` or unused `scorer`/`time` in the poller, remove the dangling import.

- [ ] **Step 6: Full test suite + vet + format**

Run: `go test ./... && go vet ./... && gofumpt -l .`
Expected: all packages PASS; `go vet` clean; `gofumpt -l .` prints nothing.

- [ ] **Step 7: Commit**

```bash
gofumpt -w internal/poller/poller.go internal/poller/poller_test.go main.go
git add internal/poller/poller.go internal/poller/poller_test.go main.go
git commit -m "feat(poller): drop scoring to snapshot-only; wire merge webhook into main"
```

---

## Task 8: Manual smoke test + docs

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Smoke test the webhook route (no real GitHub)**

```bash
WEBHOOK_SECRET= GITHUB_TOKEN=$(gh auth token) REPOS=acme/widgets DB_PATH=/tmp/leaderboard-dev.db go run . &
sleep 2
curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/webhook/github   # expect 503 (disabled)
kill %1
```

Then with a secret set, an unsigned request must be rejected:

```bash
WEBHOOK_SECRET=testsecret GITHUB_TOKEN=$(gh auth token) REPOS=acme/widgets DB_PATH=/tmp/leaderboard-dev.db go run . &
sleep 2
curl -s -o /dev/null -w "%{http_code}\n" -X POST -H "X-GitHub-Event: pull_request" localhost:8080/webhook/github   # expect 401 (bad signature)
kill %1
```

Expected: `503` then `401`. Confirms wiring + signature gate without a real delivery.

- [ ] **Step 2: Update the README**

Document in the scoring + how-it-works sections: scoring now happens at merge via `POST /webhook/github` (HMAC-verified with `WEBHOOK_SECRET`); the poller only feeds the queue + digest; the v2 scoring table (message bump, image bonus, comment scoring); and that the leaderboard reflects merged work only. Add the GitHub webhook setup note (point the repo/org webhook at `/webhook/github`, content-type `application/json`, secret = `WEBHOOK_SECRET`, events = "Pull requests").

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document Scoring v2 + merge-driven webhook"
```

---

## Self-Review

**Spec coverage:**
- Reward thoroughness (message bump, substance, image bonus, comment scoring) → Task 1. ✅
- `has_image` detection rules → Task 1 `HasImage`. ✅
- Score-on-merge / poller stops scoring → Task 7. ✅
- `review_events.has_image` column + idempotent migration → Task 2. ✅
- `comment_events` table + `UpsertCommentEvent` + dedupe → Task 2. ✅
- `Leaderboard` unions review + comment points; `Reviews` counts reviews only → Task 2. ✅
- Single-PR GraphQL fetch (VP1) → Task 3 `FetchPullRequest`. ✅
- Webhook signature verify + event filter + scoring pass (VP3 mounting, VP4 scope) → Tasks 4 + 6. ✅
- Config `WEBHOOK_SECRET` + score-weight keys → Task 5. ✅
- All "Sample totals" rows → Task 1 `TestScore`/`TestScoreComment`. ✅
- Anti-gaming: self-review/comment 0, inline cap, base-once via `raw_hash` → Tasks 1 + 4 hashing. ✅

**Resolved open verification points:** VP1 (added targeted query), VP2 (PRAGMA table_info, not error-text), VP3 (4th `http.Handler` param mirroring `runDigest`), VP4 (issue comments only; documented above and in Global Constraints).

**Placeholder scan:** none — every code step shows full code.

**Type consistency:** `scorer.Score(Review{State,InlineComments,BodyLen,HasImage,SelfReview})`, `scorer.ScoreComment(Comment{BodyLen,HasImage,SelfComment})`, `scorer.HasImage(string)`, `store.CommentEvent`/`UpsertCommentEvent`, `github.FetchPullRequest(ctx,owner,repo,number) (FetchedPRDetail,error)`, `webhook.New(secret, fetcher, st, weights)`, `httpserver.New(st, assets, runDigest, webhook)`, `poller.New(src, st)` — all used identically across producing and consuming tasks.

**Behavioral notes for the executor:**
- The v1 `poller_test` review-scoring cases and the `scorer` short-body cases change under v2 — Tasks 1 and 7 rewrite those tests rather than patch them.
- `poller.RawHash` is deleted; the equivalent lives in `webhook.hashKey`. Nothing else referenced `poller.RawHash` (verify with `grep -rn "poller.RawHash" .` before deleting — expect no hits outside `poller.go`).
