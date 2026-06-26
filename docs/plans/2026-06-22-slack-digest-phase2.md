# Slack Digest (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a scheduled Slack digest that posts the weekly review leaderboard top-5 plus stale-PR reminders to a channel daily at 09:00 Europe/Dublin.

**Architecture:** A new isolated `internal/digest` package. A pure `BuildMessage` formatter, a `SlackAPI` interface with a stdlib-`net/http` implementation hitting `chat.postMessage`, a `Digest` orchestrator that reads the existing `store.Leaderboard`/`store.Queue`, and an in-process daily ticker. A `POST /digest/run` HTTP route triggers a digest on demand for testing. No new dependencies.

**Tech Stack:** Go 1.25 stdlib only (`net/http`, `encoding/json`, `time`, `context`), `modernc.org/sqlite` (already vendored). Tests: stdlib `testing` + `net/http/httptest`, a hand-written `mockSlack`. No external test deps.

## Global Constraints

- **No new third-party dependencies** — stdlib `net/http` for Slack, copied verbatim from spec.
- **Send-only Slack** — `chat.postMessage` via bot token only; no Socket Mode, no events.
- **Schedule:** in-process daily ticker, **09:00 Europe/Dublin** (locked decision; not a cron parser).
- **Config keys (locked):** `SLACK_BOT_TOKEN`, `DIGEST_CHANNEL_ID`, `STALE_PR_HOURS` (default `48`).
- **Manual trigger (locked):** `POST /digest/run`.
- **Content (locked):** weekly top-5 leaderboard + PRs awaiting review older than `STALE_PR_HOURS`, to `DIGEST_CHANNEL_ID`.
- **Stateless-safe:** all values derived from the store at query time; no in-process accumulation.
- **Errors:** never ignore an error; `log.Printf` inside the scheduler loop, return errors everywhere else. No `panic`.
- **Format:** `gofumpt`; exported types/funcs need doc comments.

---

## File Structure

- `internal/digest/digest.go` — `Digest` struct, `SlackAPI` interface, `BuildMessage` (pure), stale-filter helper, `Run`, `RunScheduler`, `nextNineAM`.
- `internal/digest/slack.go` — `slackClient` stdlib `net/http` implementation of `SlackAPI`.
- `internal/digest/digest_test.go` — `mockSlack`, `BuildMessage` tests, `Run` tests, `nextNineAM` tests.
- `internal/config/config.go` — add `SlackBotToken`, `DigestChannelID`, `StalePRHours` fields + load.
- `internal/config/config_test.go` — add digest-config test.
- `internal/httpserver/server.go` — add `POST /digest/run` route; `New` gains a `digestRunner` param.
- `internal/httpserver/server_test.go` — add trigger test.
- `main.go` — build `digest.Digest`, start scheduler goroutine, pass runner into `httpserver.New`.
- `.env.example` — add `STALE_PR_HOURS=48`.

### Interfaces consumed from existing code (do not redefine)

- `store.LeaderRow{Login, DisplayName, Team string; IsGuest bool; Points, Reviews int; AvgPoints float64; Rank int}`
- `store.QueueRow{Repo string; PRNumber int; Title, Author, URL string; AgeHours float64; Reviewers []store.QueueReviewer}`
- `store.QueueReviewer{Login, Status string}` — status ∈ `approved|commented|changes|pending`
- `(*store.Store).Leaderboard(window string, now time.Time) ([]store.LeaderRow, error)`
- `(*store.Store).Queue(now time.Time) ([]store.QueueRow, error)`
- `(*store.Store).UpsertPerson(store.Person)`, `UpsertReviewEvent(store.ReviewEvent)`, `UpsertPR(store.PR)` — test seeding
- `store.Open(path string) (*store.Store, error)` — `":memory:"` in tests

---

## Task 1: Stale-PR filter + `BuildMessage` formatter (pure)

**Files:**
- Create: `internal/digest/digest.go`
- Test: `internal/digest/digest_test.go`

**Interfaces:**
- Consumes: `store.LeaderRow`, `store.QueueRow`, `store.QueueReviewer`.
- Produces:
  - `func isAwaiting(q store.QueueRow) bool`
  - `func BuildMessage(leaders []store.LeaderRow, stale []store.QueueRow, now time.Time, staleHours float64) string`

- [ ] **Step 1: Write the failing test**

```go
// internal/digest/digest_test.go
package digest

import (
	"strings"
	"testing"
	"time"

	"pr-review-dashboard/internal/store"
)

func TestIsAwaiting(t *testing.T) {
	cases := []struct {
		name string
		row  store.QueueRow
		want bool
	}{
		{"no reviewers", store.QueueRow{}, true},
		{"a pending reviewer", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "pending"}}}, true},
		{"all approved", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "approved"}}}, false},
		{"approved + pending", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "approved"}, {Login: "b", Status: "pending"}}}, true},
	}
	for _, c := range cases {
		if got := isAwaiting(c.row); got != c.want {
			t.Errorf("%s: isAwaiting = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildMessageHasContent(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{
		{Login: "alice", DisplayName: "Alice", Points: 24, Reviews: 6, Rank: 1},
		{Login: "bob", DisplayName: "Bob", Points: 18, Reviews: 4, Rank: 2},
	}
	stale := []store.QueueRow{
		{Repo: "acme/widgets", PRNumber: 42, Title: "Add foo", Author: "carol", URL: "https://gh/42", AgeHours: 52},
	}
	msg := BuildMessage(leaders, stale, now, 48)

	for _, want := range []string{"Alice", "24", "Bob", "acme/widgets#42", "Add foo", "carol", "52h", "https://gh/42"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
}

func TestBuildMessageAllCaughtUp(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{{Login: "alice", DisplayName: "Alice", Points: 5, Reviews: 1, Rank: 1}}
	msg := BuildMessage(leaders, nil, now, 48)
	if !strings.Contains(msg, "No PRs") {
		t.Errorf("expected all-caught-up line, got:\n%s", msg)
	}
}

func TestBuildMessageNoReviews(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{{Login: "alice", DisplayName: "Alice", Points: 0, Reviews: 0, Rank: 1}}
	msg := BuildMessage(leaders, nil, now, 48)
	if !strings.Contains(msg, "No reviews") {
		t.Errorf("expected no-reviews line, got:\n%s", msg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/digest/ -run TestIsAwaiting -v`
Expected: FAIL — `undefined: isAwaiting` / package has no non-test files.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/digest/digest.go

// Package digest builds and posts the scheduled Slack review digest.
package digest

import (
	"fmt"
	"strings"
	"time"

	"pr-review-dashboard/internal/store"
)

// topN caps how many leaders the digest lists.
const topN = 5

// isAwaiting reports whether a queued PR still needs a review: it has no
// reviewers yet, or at least one requested reviewer has not reviewed.
func isAwaiting(q store.QueueRow) bool {
	if len(q.Reviewers) == 0 {
		return true
	}
	for _, rv := range q.Reviewers {
		if rv.Status == "pending" {
			return true
		}
	}
	return false
}

// BuildMessage renders the Slack mrkdwn digest: the weekly top-N leaderboard
// followed by stale PRs awaiting review. stale is assumed pre-filtered to PRs
// older than staleHours that are still awaiting review.
func BuildMessage(leaders []store.LeaderRow, stale []store.QueueRow, now time.Time, staleHours float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*PR Review Leaderboard — week of %s*\n\n", now.Format("Mon 02 Jan"))

	b.WriteString("🏆 *Top reviewers this week*\n")
	shown := 0
	for _, l := range leaders {
		if l.Points <= 0 {
			continue
		}
		shown++
		name := l.DisplayName
		if name == "" {
			name = l.Login
		}
		fmt.Fprintf(&b, "%d. %s — %d pts (%d reviews)\n", shown, name, l.Points, l.Reviews)
		if shown >= topN {
			break
		}
	}
	if shown == 0 {
		b.WriteString("_No reviews logged yet this week._\n")
	}

	fmt.Fprintf(&b, "\n⏳ *PRs awaiting review > %dh*\n", int(staleHours))
	if len(stale) == 0 {
		fmt.Fprintf(&b, "✅ No PRs waiting longer than %dh — nice work!\n", int(staleHours))
		return b.String()
	}
	for _, q := range stale {
		name := q.Title
		fmt.Fprintf(&b, "• %s#%d _%s_ by %s — %dh %s\n",
			q.Repo, q.PRNumber, name, q.Author, int(q.AgeHours), q.URL)
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS — all four tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/digest/digest.go internal/digest/digest_test.go
git add internal/digest/digest.go internal/digest/digest_test.go
git commit -m "feat(digest): pure BuildMessage formatter + stale-PR filter"
```

---

## Task 2: `Digest` orchestrator + `Run`

**Files:**
- Modify: `internal/digest/digest.go`
- Test: `internal/digest/digest_test.go`

**Interfaces:**
- Consumes: `(*store.Store).Leaderboard`, `(*store.Store).Queue`, `BuildMessage`, `isAwaiting`.
- Produces:
  - `type SlackAPI interface { PostMessage(ctx context.Context, channel, text string) error }`
  - `type Digest struct { ... }`
  - `func New(st *store.Store, slack SlackAPI, channel string, staleHours float64) *Digest`
  - `func (d *Digest) Run(ctx context.Context, now time.Time) error`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/digest/digest_test.go
import (
	"context" // add to existing import block
)

type mockSlack struct {
	channel string
	text    string
	calls   int
	err     error
}

func (m *mockSlack) PostMessage(_ context.Context, channel, text string) error {
	m.calls++
	m.channel = channel
	m.text = text
	return m.err
}

func TestRunPostsDigest(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "acme/widgets", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 4, RawHash: "h1", SubmittedAt: now})
	// A stale PR: ready 72h ago, no reviews -> awaiting.
	st.UpsertPR(store.PR{Repo: "acme/widgets", PRNumber: 9, Title: "Stale one", Author: "carol", URL: "https://gh/9", IsDraft: false, ReadyAt: now.Add(-72 * time.Hour).Format(time.RFC3339), MergedAt: ""})

	m := &mockSlack{}
	d := New(st, m, "C123", 48)
	if err := d.Run(context.Background(), now); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("PostMessage calls = %d, want 1", m.calls)
	}
	if m.channel != "C123" {
		t.Errorf("channel = %q, want C123", m.channel)
	}
	if !strings.Contains(m.text, "Alice") || !strings.Contains(m.text, "acme/widgets#9") {
		t.Errorf("digest text missing expected content:\n%s", m.text)
	}
}

func TestRunAllCaughtUp(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})

	m := &mockSlack{}
	d := New(st, m, "C123", 48)
	if err := d.Run(context.Background(), now); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(m.text, "No PRs waiting") {
		t.Errorf("expected all-caught-up text, got:\n%s", m.text)
	}
}
```

> NOTE: confirm `store.PR` field names before running (`grep -n "type PR struct" -A12 internal/store/store.go`). If a field differs (e.g. `Ready` vs `ReadyAt`), fix the seed line — do not change the store.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/digest/ -run TestRun -v`
Expected: FAIL — `undefined: New` / `undefined: SlackAPI`.

- [ ] **Step 3: Write minimal implementation**

```go
// append to internal/digest/digest.go
import (
	"context" // add to existing import block
)

// SlackAPI is the send-only Slack surface the digest needs.
type SlackAPI interface {
	PostMessage(ctx context.Context, channel, text string) error
}

// Digest builds the weekly digest from the store and posts it to Slack.
type Digest struct {
	store      *store.Store
	slack      SlackAPI
	channel    string
	staleHours float64
}

// New constructs a Digest. channel is the Slack channel ID; staleHours is the
// age threshold for flagging a PR as awaiting review.
func New(st *store.Store, slack SlackAPI, channel string, staleHours float64) *Digest {
	return &Digest{store: st, slack: slack, channel: channel, staleHours: staleHours}
}

// Run builds the digest for the given instant and posts it to Slack.
func (d *Digest) Run(ctx context.Context, now time.Time) error {
	leaders, err := d.store.Leaderboard("week", now)
	if err != nil {
		return fmt.Errorf("leaderboard: %w", err)
	}
	queue, err := d.store.Queue(now)
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	var stale []store.QueueRow
	for _, q := range queue {
		if q.AgeHours > d.staleHours && isAwaiting(q) {
			stale = append(stale, q)
		}
	}
	return d.slack.PostMessage(ctx, d.channel, BuildMessage(leaders, stale, now, d.staleHours))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/digest/digest.go internal/digest/digest_test.go
git add internal/digest/digest.go internal/digest/digest_test.go
git commit -m "feat(digest): Digest orchestrator with Run + SlackAPI interface"
```

---

## Task 3: `nextNineAM` + `RunScheduler` (in-process daily ticker)

**Files:**
- Modify: `internal/digest/digest.go`
- Test: `internal/digest/digest_test.go`

**Interfaces:**
- Consumes: `(*Digest).Run`.
- Produces:
  - `func nextNineAM(now time.Time, loc *time.Location) time.Time`
  - `func (d *Digest) RunScheduler(ctx context.Context, nowFn func() time.Time)`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/digest/digest_test.go
func TestNextNineAM(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		t.Fatalf("load loc: %v", err)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			"before 9am same day",
			time.Date(2026, 6, 22, 7, 30, 0, 0, loc),
			time.Date(2026, 6, 22, 9, 0, 0, 0, loc),
		},
		{
			"after 9am rolls to tomorrow",
			time.Date(2026, 6, 22, 10, 0, 0, 0, loc),
			time.Date(2026, 6, 23, 9, 0, 0, 0, loc),
		},
		{
			"exactly 9am rolls to tomorrow",
			time.Date(2026, 6, 22, 9, 0, 0, 0, loc),
			time.Date(2026, 6, 23, 9, 0, 0, 0, loc),
		},
	}
	for _, c := range cases {
		if got := nextNineAM(c.now, loc); !got.Equal(c.want) {
			t.Errorf("%s: nextNineAM = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/digest/ -run TestNextNineAM -v`
Expected: FAIL — `undefined: nextNineAM`.

- [ ] **Step 3: Write minimal implementation**

```go
// append to internal/digest/digest.go
import (
	"log" // add to existing import block
)

// dublin returns the Europe/Dublin location, falling back to UTC if the tzdata
// is unavailable.
func dublin() *time.Location {
	loc, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		return time.UTC
	}
	return loc
}

// nextNineAM returns the next 09:00 in loc strictly after now.
func nextNineAM(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), 9, 0, 0, 0, loc)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// RunScheduler blocks, firing Run once per day at 09:00 Europe/Dublin until ctx
// is cancelled. nowFn supplies the current time (injectable for tests).
func (d *Digest) RunScheduler(ctx context.Context, nowFn func() time.Time) {
	loc := dublin()
	for {
		wait := nextNineAM(nowFn(), loc).Sub(nowFn())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			if err := d.Run(rctx, nowFn()); err != nil {
				log.Printf("digest run: %v", err)
			}
			cancel()
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS — including `TestNextNineAM`.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/digest/digest.go internal/digest/digest_test.go
git add internal/digest/digest.go internal/digest/digest_test.go
git commit -m "feat(digest): daily 09:00 Europe/Dublin in-process scheduler"
```

---

## Task 4: `slackClient` — stdlib `chat.postMessage`

**Files:**
- Create: `internal/digest/slack.go`
- Test: `internal/digest/slack_test.go`

**Interfaces:**
- Consumes: `SlackAPI` (implements it).
- Produces:
  - `func NewSlackClient(token string) *slackClient`
  - `func (c *slackClient) PostMessage(ctx context.Context, channel, text string) error`
  - unexported `endpoint` field overridable in tests.

- [ ] **Step 1: Write the failing test**

```go
// internal/digest/slack_test.go
package digest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackClientPostMessage(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := NewSlackClient("xoxb-test")
	c.endpoint = srv.URL
	if err := c.PostMessage(context.Background(), "C123", "hello *world*"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, gotBody)
	}
	if payload["channel"] != "C123" || payload["text"] != "hello *world*" {
		t.Errorf("payload = %v", payload)
	}
}

func TestSlackClientAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()

	c := NewSlackClient("xoxb-test")
	c.endpoint = srv.URL
	err := c.PostMessage(context.Background(), "C123", "hi")
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("expected channel_not_found error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/digest/ -run TestSlackClient -v`
Expected: FAIL — `undefined: NewSlackClient`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/digest/slack.go
package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultSlackEndpoint = "https://slack.com/api/chat.postMessage"

// slackClient posts messages via the Slack Web API using a bot token.
type slackClient struct {
	token      string
	endpoint   string
	httpClient *http.Client
}

// NewSlackClient returns a send-only Slack client authenticated with a bot token.
func NewSlackClient(token string) *slackClient {
	return &slackClient{
		token:      token,
		endpoint:   defaultSlackEndpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// PostMessage posts text to a channel via chat.postMessage. Slack returns HTTP
// 200 even on logical errors, so the JSON "ok" field is checked.
func (c *slackClient) PostMessage(ctx context.Context, channel, text string) error {
	body, err := json.Marshal(map[string]string{"channel": channel, "text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: http %d", resp.StatusCode)
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("slack: decode response: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("slack: %s", out.Error)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS — Slack client tests green; `*slackClient` satisfies `SlackAPI`.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/digest/slack.go internal/digest/slack_test.go
git add internal/digest/slack.go internal/digest/slack_test.go
git commit -m "feat(digest): stdlib Slack chat.postMessage client"
```

---

## Task 5: Config — `SLACK_BOT_TOKEN`, `DIGEST_CHANNEL_ID`, `STALE_PR_HOURS`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `.env.example`

**Interfaces:**
- Produces (added to `config.Config`): `SlackBotToken string`, `DigestChannelID string`, `StalePRHours float64`.

- [ ] **Step 1: Write the failing test**

```go
// append to internal/config/config_test.go (inside the existing package)
func TestLoadDigestConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-1")
	t.Setenv("DIGEST_CHANNEL_ID", "C999")
	t.Setenv("STALE_PR_HOURS", "24")

	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SlackBotToken != "xoxb-1" {
		t.Errorf("SlackBotToken = %q", c.SlackBotToken)
	}
	if c.DigestChannelID != "C999" {
		t.Errorf("DigestChannelID = %q", c.DigestChannelID)
	}
	if c.StalePRHours != 24 {
		t.Errorf("StalePRHours = %v, want 24", c.StalePRHours)
	}
}

func TestLoadStalePRHoursDefault(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("STALE_PR_HOURS", "")
	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.StalePRHours != 48 {
		t.Errorf("StalePRHours default = %v, want 48", c.StalePRHours)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadDigest -v`
Expected: FAIL — `c.SlackBotToken undefined`.

- [ ] **Step 3: Write minimal implementation**

Add three fields to the `Config` struct (after `Weights scorer.Weights`):

```go
	SlackBotToken   string
	DigestChannelID string
	StalePRHours    float64
```

In `Load`, set them in the `c := Config{...}` literal:

```go
		SlackBotToken:   os.Getenv("SLACK_BOT_TOKEN"),
		DigestChannelID: os.Getenv("DIGEST_CHANNEL_ID"),
		StalePRHours:    floatOr("STALE_PR_HOURS", 48),
```

Add the helper at the bottom of the file:

```go
func floatOr(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
```

Add `"strconv"` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all config tests green.

- [ ] **Step 5: Update `.env.example`**

Add under the Slack block (after `DIGEST_CHANNEL_ID=`):

```
# Hours a PR can wait for review before the digest flags it as stale
STALE_PR_HOURS=48
```

- [ ] **Step 6: Commit**

```bash
gofumpt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go .env.example
git commit -m "feat(config): SLACK_BOT_TOKEN, DIGEST_CHANNEL_ID, STALE_PR_HOURS"
```

---

## Task 6: `POST /digest/run` HTTP trigger

**Files:**
- Modify: `internal/httpserver/server.go`
- Modify: `internal/httpserver/server_test.go`

**Interfaces:**
- Consumes: a runner abstraction so the server does not import `digest` directly (and tests need no Slack):
  - `New` signature becomes `New(st *store.Store, assets fs.FS, runDigest func(context.Context) error) http.Handler`
  - `runDigest` may be `nil` (digest disabled) → route returns `503`.
- The `*digest.Digest` from Task 2 is adapted in `main.go` (Task 7) as `func(ctx context.Context) error { return d.Run(ctx, time.Now()) }`.

- [ ] **Step 1: Write the failing test**

```go
// append to internal/httpserver/server_test.go
import (
	"context" // add to existing import block
	"sync/atomic"
)

func TestDigestRunTrigger(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()

	var called atomic.Int32
	run := func(_ context.Context) error {
		called.Add(1)
		return nil
	}
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, run)

	req := httptest.NewRequest(http.MethodPost, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if called.Load() != 1 {
		t.Errorf("runDigest called %d times, want 1", called.Load())
	}
}

func TestDigestRunDisabled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil)

	req := httptest.NewRequest(http.MethodPost, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestDigestRunRejectsGET(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, func(_ context.Context) error { return nil })

	req := httptest.NewRequest(http.MethodGet, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
```

> NOTE: the two existing tests (`TestLeaderboardEndpoint`, `TestHealthEndpoint`) call `New(st, assets)` with two args. Update both call sites to pass `nil` as the third arg in the same step you change the signature, or they won't compile.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpserver/ -v`
Expected: FAIL — `not enough arguments in call to New`.

- [ ] **Step 3: Write minimal implementation**

Change `New`'s signature and add the route. In `internal/httpserver/server.go`:

```go
// New returns the HTTP handler. assets is the built Vue dashboard filesystem.
// runDigest triggers an on-demand Slack digest; pass nil to disable the route.
func New(st *store.Store, assets fs.FS, runDigest func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
```

Add `"context"` to the import block. Register the route (before `mux.Handle("/", ...)`):

```go
	mux.HandleFunc("/digest/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if runDigest == nil {
			http.Error(w, "digest not configured", http.StatusServiceUnavailable)
			return
		}
		if err := runDigest(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("digest sent"))
	})
```

Update the two existing test call sites in `server_test.go` to `New(st, assets, nil)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpserver/ -v`
Expected: PASS — old + new tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/httpserver/server.go internal/httpserver/server_test.go
git add internal/httpserver/server.go internal/httpserver/server_test.go
git commit -m "feat(httpserver): POST /digest/run manual trigger"
```

---

## Task 7: Wire digest into `main.go`

**Files:**
- Modify: `main.go`

**Interfaces:**
- Consumes: `config.Config.{SlackBotToken, DigestChannelID, StalePRHours}`, `digest.New`, `digest.NewSlackClient`, `(*digest.Digest).Run`, `(*digest.Digest).RunScheduler`, updated `httpserver.New`.

- [ ] **Step 1: Build digest + scheduler + runner, pass into server**

Edit `main.go`. Add `"pr-review-dashboard/internal/digest"` to imports. After the poller goroutine block and before `h := httpserver.New(...)`:

```go
	// Slack digest: enabled only when a bot token and channel are configured.
	var runDigest func(context.Context) error
	if cfg.SlackBotToken != "" && cfg.DigestChannelID != "" {
		dg := digest.New(st, digest.NewSlackClient(cfg.SlackBotToken), cfg.DigestChannelID, cfg.StalePRHours)
		runDigest = func(ctx context.Context) error { return dg.Run(ctx, time.Now()) }
		go dg.RunScheduler(context.Background(), time.Now)
		log.Printf("digest scheduler enabled for channel %s (09:00 Europe/Dublin)", cfg.DigestChannelID)
	} else {
		log.Print("digest disabled: set SLACK_BOT_TOKEN and DIGEST_CHANNEL_ID to enable")
	}

	h := httpserver.New(st, httpserver.Assets(), runDigest)
```

Remove the now-duplicated `h := httpserver.New(st, httpserver.Assets())` line.

- [ ] **Step 2: Build the whole binary**

Run: `go build ./...`
Expected: success, no errors.

- [ ] **Step 3: Full test suite**

Run: `go test ./...`
Expected: all packages PASS.

- [ ] **Step 4: Vet + format check**

Run: `go vet ./... && gofumpt -l .`
Expected: `go vet` clean; `gofumpt -l .` prints nothing (no unformatted files).

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat(digest): wire scheduler + /digest/run into main"
```

---

## Task 8: Manual smoke test + docs

**Files:**
- Modify: `docs/specs/2026-06-22-pr-review-leaderboard-design.md` (mark Phase 2 done) — optional
- Modify: `README.md` (digest section) — if a feature list exists

- [ ] **Step 1: Smoke test the trigger locally (no real Slack)**

Run the binary against an in-memory/dev DB without Slack creds and confirm the route reports disabled:

```bash
SLACK_BOT_TOKEN= DIGEST_CHANNEL_ID= GITHUB_TOKEN=$(gh auth token) REPOS=acme/widgets,acme/gadgets DB_PATH=/tmp/leaderboard-dev.db go run . &
sleep 2
curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/digest/run   # expect 503
kill %1
```

Expected: `503` (digest disabled — no creds). Confirms wiring without posting to Slack.

- [ ] **Step 2: (Optional) real Slack dry run**

With a real bot token + a test channel the bot is invited to:

```bash
SLACK_BOT_TOKEN=xoxb-… DIGEST_CHANNEL_ID=C… GITHUB_TOKEN=$(gh auth token) REPOS=… DB_PATH=/tmp/leaderboard-dev.db go run . &
sleep 5   # let one poll populate the DB
curl -s -X POST localhost:8080/digest/run   # expect "digest sent"; check the channel
kill %1
```

- [ ] **Step 3: Update README digest section (if present)**

Document: enable by setting `SLACK_BOT_TOKEN` + `DIGEST_CHANNEL_ID`; invite the bot to the channel; daily 09:00 Europe/Dublin; manual `POST /digest/run`; `STALE_PR_HOURS` tunable.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/specs/2026-06-22-pr-review-leaderboard-design.md
git commit -m "docs(digest): document Slack digest enablement + trigger"
```

---

## Self-Review

**Spec coverage (§ "Slack digest (Phase 2)"):**
- Daily 09:00 Europe/Dublin → Task 3 `nextNineAM` + `RunScheduler`. ✅
- Bot token `chat.postMessage`, no Socket Mode → Task 4 `slackClient`. ✅
- Target channel `DIGEST_CHANNEL_ID`, bot invited → Task 5 config + Task 8 docs. ✅
- Content = weekly top-5 + stale PRs > `STALE_PR_HOURS` → Task 1 `BuildMessage` + Task 2 stale filter. ✅
- Test via `mockSlack` implementing `SlackAPI`; "has PRs" + "all caught up" cases → Task 2 `TestRunPostsDigest` / `TestRunAllCaughtUp`. ✅
- Stdlib `testing` only, no external deps → all tests use `testing`/`httptest`. ✅
- Locked decisions (in-process ticker, `POST /digest/run`) → Tasks 3 + 6. ✅

**Placeholder scan:** none — every code step shows full code; no TBD/TODO.

**Type consistency:** `SlackAPI.PostMessage(ctx, channel, text)` identical in Task 2 (def), Task 2 mock, Task 4 impl. `New(st, slack, channel, staleHours)` used consistently. `BuildMessage(leaders, stale, now, staleHours)` signature identical across Tasks 1–2. `httpserver.New(st, assets, runDigest)` updated at all three call sites (Task 6 + Task 7). `store.PR` field names flagged for verification in Task 2 note.

---

## Open verification points (resolve during execution, don't guess)

1. **`store.PR` / `store.Person` / `store.ReviewEvent` field names** — confirm with `grep -n "type PR struct" -A14 internal/store/store.go` before writing Task 2's seed. Plan assumes `PR{Repo, PRNumber, Title, Author, URL, IsDraft, ReadyAt, MergedAt}`, `Person{Login, DisplayName, Team, Active}`, `ReviewEvent{Repo, PRNumber, Reviewer, State, Points, RawHash, SubmittedAt}` (the last two match `server_test.go` usage already).
2. **`UpsertPR` / `UpsertReviewEvent` return values** — `server_test.go` ignores them; tests here also ignore. If lint forbids ignored errors in tests, assign to `_ =` or check.
