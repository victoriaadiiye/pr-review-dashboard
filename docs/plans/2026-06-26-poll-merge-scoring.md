# Poll-Based Merge Scoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Score merged PRs by polling GitHub on the existing poll loop instead of via a webhook — the first scan of a repo backfills the last 30 days, every later scan ingests only new merges.

**Architecture:** Extract the per-PR scoring pass out of the webhook handler into a shared `internal/ingest` package. A new `internal/mergescan` package resolves a per-repo high-water mark (stored in a new `meta` table), lists merged PRs since then via a new GitHub query, and scores each through `ingest`. `main` calls the scanner inside the existing poll goroutine. The webhook is kept, slimmed to delegate to `ingest`, and stays off unless `WEBHOOK_SECRET` is set.

**Tech Stack:** Go 1.25 stdlib only (`net/http`, `crypto/sha256`, `encoding/json`, `time`, `database/sql`), `modernc.org/sqlite` (vendored). Tests: stdlib `testing` + `net/http/httptest` + hand-written fakes. No new dependencies.

## Global Constraints

- **No new third-party dependencies** — stdlib only.
- **Scoring happens at merge.** Reviews/comments score 0 until the PR merges; closed-without-merge never scores. Unchanged from scoring-v2.
- **Trigger is the poll loop**, not a webhook. The webhook is retained but optional (enabled only when `WEBHOOK_SECRET` set; 503 otherwise).
- **Backfill = first scan.** No high-water mark for a repo → window is the last `BACKFILL_DAYS` days. No separate startup path, no "empty DB" check.
- **Config (locked):** `BACKFILL_DAYS` (default `30`; `0` or negative disables all merge-scanning and backfill).
- **High-water mark** stored per repo in a `meta(key, value)` table, key `last_merge_scan:<owner/name>`, value RFC3339 UTC; a 1-hour overlap is applied on read.
- **Idempotency:** all scoring upserts dedupe on `raw_hash`; overlapping windows and re-scans never double-count.
- **One scoring path:** both the scanner and the webhook call `ingest.Ingester.ScorePR`; scoring must not be duplicated.
- **Errors:** never ignored; a failed list or score leaves the mark unadvanced (window retries next cycle); `main` logs per-repo and continues. No `panic`.
- **Format:** `gofumpt`; exported types/funcs documented.

---

## File Structure

- `internal/store/store.go` — add `meta` table to schema; add `GetMeta`/`SetMeta`. (Task 1)
- `internal/github/client.go` — add `FetchMergedPRNumbers` + exported `SplitRepo` helper. (Task 2)
- `internal/ingest/ingest.go` + `_test.go` — NEW: `Ingester`, `PRFetcher`, `ScorePR` (moved from webhook) + moved scoring tests. (Task 3)
- `internal/webhook/webhook.go` + `_test.go` — slim to delegate to `ingest`; update tests. (Task 3)
- `main.go` — shared `gh` client + `ing`; repoint webhook (Task 3); add scanner to poll loop (Task 6).
- `internal/mergescan/mergescan.go` + `_test.go` — NEW: `Scanner.ScanRepo`. (Task 4)
- `internal/config/config.go` + `_test.go`, `.env.example` — add `BACKFILL_DAYS`. (Task 5)
- `README.md` — note scoring is poll-driven. (Task 6)

---

## Task 1: Store — `meta` table + `GetMeta`/`SetMeta`

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go` (append)

**Interfaces:**
- Consumes: existing `Store.db (*sql.DB)`, `database/sql` (already imported).
- Produces:
  - `func (s *Store) GetMeta(key string) (string, bool, error)` — bool is found
  - `func (s *Store) SetMeta(key, value string) error`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestMetaRoundTrip(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if _, found, err := st.GetMeta("k"); err != nil || found {
		t.Fatalf("miss: found=%v err=%v, want found=false nil", found, err)
	}
	if err := st.SetMeta("k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, found, err := st.GetMeta("k")
	if err != nil || !found || v != "v1" {
		t.Fatalf("hit: v=%q found=%v err=%v, want v1 true nil", v, found, err)
	}
	if err := st.SetMeta("k", "v2"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if v, _, _ := st.GetMeta("k"); v != "v2" {
		t.Errorf("after overwrite v=%q, want v2", v)
	}
}
```

> NOTE: `store_test.go` is `package store` (internal) and already imports `testing`. Do not add a `store.` prefix or re-import.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMetaRoundTrip -v`
Expected: FAIL — `st.GetMeta undefined`.

- [ ] **Step 3: Implement**

In `internal/store/store.go`, add the table to the `schema` const (after the `comment_events` block, before the closing backtick):

```go
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
```

Add the methods (after `UpsertCommentEvent`):

```go
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
```

(`database/sql` is already imported in `store.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS — `TestMetaRoundTrip` green; all existing store tests still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/store/store.go internal/store/store_test.go
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): meta key/value table + GetMeta/SetMeta"
```

---

## Task 2: GitHub — `FetchMergedPRNumbers` + `SplitRepo`

**Files:**
- Modify: `internal/github/client.go`
- Test: `internal/github/client_test.go` (append)

**Interfaces:**
- Consumes: existing `Client.do`, `time`, `strings` (already imported).
- Produces:
  - `func (c *Client) FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error)` — merged-PR numbers newest-first, only those merged on/after `since`.
  - `func SplitRepo(full string) (owner, name string, ok bool)` — shared "owner/name" splitter.

- [ ] **Step 1: Write the failing test**

Append to `internal/github/client_test.go` (ensure `time` is in the import block; add if missing):

```go
func TestFetchMergedPRNumbers(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Node A: merged & updated after since -> included.
	// Node B: updated after since but merged before since -> excluded, keep going.
	// Node C: updated before since -> stop (and excluded).
	body := `{"data":{"repository":{"pullRequests":{
		"nodes":[
			{"number":50,"mergedAt":"2026-06-10T10:00:00Z","updatedAt":"2026-06-10T11:00:00Z"},
			{"number":40,"mergedAt":"2026-05-01T10:00:00Z","updatedAt":"2026-06-05T09:00:00Z"},
			{"number":30,"mergedAt":"2026-04-01T10:00:00Z","updatedAt":"2026-05-20T09:00:00Z"}
		],
		"pageInfo":{"hasNextPage":false,"endCursor":null}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	got, err := c.FetchMergedPRNumbers(context.Background(), "acme", "widgets", since)
	if err != nil {
		t.Fatalf("FetchMergedPRNumbers: %v", err)
	}
	if len(got) != 1 || got[0] != 50 {
		t.Errorf("got %v, want [50] (40 merged before since; 30 stops the scan)", got)
	}
}

func TestSplitRepo(t *testing.T) {
	cases := []struct {
		in            string
		owner, name   string
		ok            bool
	}{
		{"acme/widgets", "acme", "widgets", true},
		{"noslash", "", "", false},
		{"/leading", "", "", false},
		{"trailing/", "", "", false},
	}
	for _, c := range cases {
		o, n, ok := SplitRepo(c.in)
		if o != c.owner || n != c.name || ok != c.ok {
			t.Errorf("SplitRepo(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, o, n, ok, c.owner, c.name, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/github/ -run 'TestFetchMergedPRNumbers|TestSplitRepo' -v`
Expected: FAIL — `c.FetchMergedPRNumbers undefined` / `undefined: SplitRepo`.

- [ ] **Step 3: Implement**

In `internal/github/client.go`, add (after `FetchPullRequest` / its parse struct):

```go
const mergedPRQuery = `
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:MERGED,first:50,after:$cursor,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{ number mergedAt updatedAt }
      pageInfo{ hasNextPage endCursor }
    }
  }
}`

type mergedPRGQL struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number    int        `json:"number"`
					MergedAt  *time.Time `json:"mergedAt"`
					UpdatedAt time.Time  `json:"updatedAt"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchMergedPRNumbers returns the numbers of PRs merged on/after since,
// newest-first. It pages pullRequests(states:MERGED) ordered by UPDATED_AT desc,
// collecting numbers whose mergedAt >= since, and stops paging once a node's
// updatedAt < since (safe because updatedAt >= mergedAt for any PR).
func (c *Client) FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error) {
	var out []int
	var cursor *string
	for {
		var resp mergedPRGQL
		vars := map[string]any{"owner": owner, "repo": repo, "cursor": cursor}
		if err := c.do(ctx, mergedPRQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Data.Repository.PullRequests.Nodes {
			if n.UpdatedAt.Before(since) {
				return out, nil // all remaining are older; stop
			}
			if n.MergedAt != nil && !n.MergedAt.Before(since) {
				out = append(out, n.Number)
			}
		}
		pi := resp.Data.Repository.PullRequests.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}

// SplitRepo splits "owner/name" into its parts. ok is false if malformed.
func SplitRepo(full string) (owner, name string, ok bool) {
	i := strings.IndexByte(full, '/')
	if i <= 0 || i == len(full)-1 {
		return "", "", false
	}
	return full[:i], full[i+1:], true
}
```

(`time` and `strings` are already imported in `client.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/github/ -v`
Expected: PASS — new tests green; existing client tests still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/github/client.go internal/github/client_test.go
git add internal/github/client.go internal/github/client_test.go
git commit -m "feat(github): FetchMergedPRNumbers + SplitRepo helper"
```

---

## Task 3: Extract scoring pass into `internal/ingest`; repoint webhook

**Files:**
- Create: `internal/ingest/ingest.go`
- Create: `internal/ingest/ingest_test.go`
- Modify: `internal/webhook/webhook.go`
- Modify: `internal/webhook/webhook_test.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `github.FetchedPRDetail`, `scorer.{Score,ScoreComment,HasImage,Review,Comment,Weights}`, `store.{ReviewEvent,CommentEvent,Person,UpsertReviewEvent,UpsertCommentEvent,EnsurePerson}`.
- Produces:
  - `type ingest.PRFetcher interface { FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error) }`
  - `func ingest.New(fetcher PRFetcher, st *store.Store, w scorer.Weights) *Ingester`
  - `func (i *Ingester) ScorePR(ctx context.Context, fullName, owner, repo string, number int) error`
- After this task `webhook.New` becomes `func New(secret string, ing *ingest.Ingester) http.Handler`.

- [ ] **Step 1: Create `internal/ingest/ingest.go`** (the scoring pass moved verbatim from `webhook.go`)

```go
// Package ingest scores a single merged PR's reviews and comments into the
// store. It is the shared scoring pass used by both the merge scanner and the
// optional webhook, so scoring cannot diverge between triggers.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
	"pr-review-dashboard/internal/github"
)

// PRFetcher fetches a single PR's review + comment history (test seam).
type PRFetcher interface {
	FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error)
}

// Ingester scores merged PRs into the store.
type Ingester struct {
	fetcher PRFetcher
	st      *store.Store
	weights scorer.Weights
}

// New constructs an Ingester.
func New(fetcher PRFetcher, st *store.Store, w scorer.Weights) *Ingester {
	return &Ingester{fetcher: fetcher, st: st, weights: w}
}

// ScorePR fetches one PR's reviews + issue comments, scores each, and upserts
// them. fullName is the "owner/repo" string stored on each event row; owner and
// repo are its split halves used for the fetch. Idempotent via raw_hash.
// Self-authored events score 0 but are still stored; empty-author
// (deleted-account) events are skipped entirely.
func (i *Ingester) ScorePR(ctx context.Context, fullName, owner, repo string, number int) error {
	d, err := i.fetcher.FetchPullRequest(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, rv := range d.Reviews {
		if rv.Author == "" {
			continue
		}
		hasImg := scorer.HasImage(rv.Body)
		pts := scorer.Score(scorer.Review{
			State: rv.State, InlineComments: rv.InlineComments, BodyLen: len(rv.Body),
			HasImage: hasImg, SelfReview: rv.Author == d.Author,
		}, i.weights)
		if err := i.st.UpsertReviewEvent(store.ReviewEvent{
			Repo: fullName, PRNumber: number, Reviewer: rv.Author, State: rv.State,
			InlineComments: rv.InlineComments, BodyLen: len(rv.Body), SubmittedAt: rv.SubmittedAt,
			Points: pts, HasImage: hasImg,
			RawHash: hashKey(fullName, number, rv.Author, "review", rv.ID, len(rv.Body)),
		}); err != nil {
			return fmt.Errorf("upsert review %s: %w", rv.ID, err)
		}
		if err := i.seedGuest(rv.Author); err != nil {
			return err
		}
	}
	for _, cm := range d.Comments {
		if cm.Author == "" {
			continue
		}
		hasImg := scorer.HasImage(cm.Body)
		pts := scorer.ScoreComment(scorer.Comment{
			BodyLen: len(cm.Body), HasImage: hasImg, SelfComment: cm.Author == d.Author,
		}, i.weights)
		if err := i.st.UpsertCommentEvent(store.CommentEvent{
			Repo: fullName, PRNumber: number, Author: cm.Author, Kind: "issue",
			BodyLen: len(cm.Body), HasImage: hasImg, CreatedAt: cm.CreatedAt, Points: pts,
			RawHash: hashKey(fullName, number, cm.Author, "issue", cm.ID, len(cm.Body)),
		}); err != nil {
			return fmt.Errorf("upsert comment %s: %w", cm.ID, err)
		}
		if err := i.seedGuest(cm.Author); err != nil {
			return err
		}
	}
	return nil
}

// seedGuest records an actor as a guest if not already on the roster. Never
// downgrades an existing member (EnsurePerson is insert-if-absent).
func (i *Ingester) seedGuest(login string) error {
	if login == "" {
		return nil
	}
	return i.st.EnsurePerson(store.Person{Login: login, DisplayName: login, Team: "guest", Active: true})
}

// hashKey is the idempotency key for an event row. Including bodyLen means an
// edited body re-scores; the stable node id prevents double-counting redelivery.
func hashKey(repo string, pr int, actor, kind, id string, bodyLen int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, actor, kind, id, bodyLen)))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 2: Create `internal/ingest/ingest_test.go`** (scoring-detail tests moved from `webhook_test.go`)

```go
package ingest

import (
	"context"
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

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestScorePRPersists(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 42, Author: "carol",
		Reviews: []github.FetchedReview{
			{ID: "R1", Author: "alice", State: "CHANGES_REQUESTED", Body: strings.Repeat("x", 300), InlineComments: 0},
		},
		Comments: []github.FetchedComment{
			{ID: "C1", Author: "bob", Body: "great"},
			{ID: "C2", Author: "carol", Body: "self comment ignored"}, // self -> 0, still stored
			{ID: "C3", Author: "", Body: "ghost"},                     // empty author -> skipped
		},
	}}
	ing := New(f, st, scorer.Default())
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 42); err != nil {
		t.Fatalf("ScorePR: %v", err)
	}

	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	pts := map[string]int{}
	for _, r := range board {
		pts[r.Login] = r.Points
	}
	if pts["alice"] != 7 { // CHANGES(2+3) + substance(2)
		t.Errorf("alice = %d, want 7", pts["alice"])
	}
	if pts["bob"] != 1 { // comment base
		t.Errorf("bob = %d, want 1", pts["bob"])
	}
	if _, ok := pts[""]; ok {
		t.Errorf("empty-author row surfaced on board: %v", pts)
	}
}

func TestScorePRIdempotent(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 1, Author: "carol",
		Reviews: []github.FetchedReview{{ID: "R1", Author: "alice", State: "APPROVED", Body: "lgtm"}},
	}}
	ing := New(f, st, scorer.Default())
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 1); err != nil {
		t.Fatalf("second: %v", err)
	}
	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	for _, r := range board {
		if r.Login == "alice" && r.Reviews != 1 {
			t.Errorf("alice Reviews = %d after double ScorePR, want 1 (deduped)", r.Reviews)
		}
	}
}

func timeNowUTC() time.Time { return time.Now().UTC() }
```

> NOTE: add `"time"` to the `ingest_test.go` import block (used by `timeNowUTC`).

- [ ] **Step 3: Run the new ingest tests to verify they fail (no impl yet built against them) then pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — `ingest.go` from Step 1 satisfies these. (If `time` import is missing it will fail to compile; add it.)

- [ ] **Step 4: Slim `internal/webhook/webhook.go` to delegate to ingest**

Replace the entire file with:

```go
// Package webhook receives GitHub pull_request events and, on merge, delegates
// scoring to the ingest package. Optional: enabled only when a secret is set.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/ingest"
)

// New returns the GitHub webhook handler. If secret is empty the route is
// disabled and returns 503. Scoring is delegated to ing.
func New(secret string, ing *ingest.Ingester) http.Handler {
	return &handler{secret: secret, ing: ing}
}

type handler struct {
	secret string
	ing    *ingest.Ingester
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
	owner, repo, ok := github.SplitRepo(ev.Repository.FullName)
	if !ok {
		http.Error(w, "bad repository.full_name", http.StatusBadRequest)
		return
	}
	if err := h.ing.ScorePR(r.Context(), ev.Repository.FullName, owner, repo, ev.PullRequest.Number); err != nil {
		log.Printf("webhook score %s#%d: %v", ev.Repository.FullName, ev.PullRequest.Number, err)
		http.Error(w, "scoring failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("scored"))
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
```

This removes `PRFetcher`, `score`, `seedGuest`, `hashKey`, the `splitFullName` helper (now `github.SplitRepo`), and the `context`/`fmt`/`scorer`/`store` imports.

- [ ] **Step 5: Update `internal/webhook/webhook_test.go`**

The webhook test now drives status/routing only; scoring detail lives in `ingest_test.go`. Replace the whole file with:

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
	"pr-review-dashboard/internal/ingest"
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

func newHandler(t *testing.T, secret string, f *fakeFetcher) http.Handler {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(secret, ingest.New(f, st, scorer.Default()))
}

func TestMergedEventDelegatesToIngest(t *testing.T) {
	f := &fakeFetcher{detail: github.FetchedPRDetail{Number: 42, Author: "carol"}}
	h := newHandler(t, "sekret", f)
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (delegated to ingest)", f.calls)
	}
}

func TestBadSignatureRejected(t *testing.T) {
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "sha256=deadbeef", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMissingSignatureRejected(t *testing.T) {
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDisabledWhenNoSecret(t *testing.T) {
	h := newHandler(t, "", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "", body); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNonMergeEventIgnored(t *testing.T) {
	f := &fakeFetcher{}
	h := newHandler(t, "sekret", f)
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
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(`{}`)
	if rec := post(t, h, "push", sign("sekret", body), body); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
```

- [ ] **Step 6: Repoint `main.go`'s webhook construction**

In `main.go`: add import `"pr-review-dashboard/internal/ingest"`. After `st` is opened and before the poller block, create a shared client + ingester:

```go
	gh := github.NewClient(cfg.GitHubToken)
	ing := ingest.New(gh, st, cfg.Weights)
```

Change the poller line to reuse `gh`:

```go
	p := poller.New(gh, st)
```

Change the webhook construction line from
`webhookHandler = webhook.New(cfg.WebhookSecret, github.NewClient(cfg.GitHubToken), st, cfg.Weights)`
to:

```go
		webhookHandler = webhook.New(cfg.WebhookSecret, ing)
```

- [ ] **Step 7: Build + run the affected suites**

Run: `go build ./... && go test ./internal/ingest/ ./internal/webhook/ -v`
Expected: build OK; ingest + webhook tests green.

- [ ] **Step 8: Commit**

```bash
gofumpt -w internal/ingest/ingest.go internal/ingest/ingest_test.go internal/webhook/webhook.go internal/webhook/webhook_test.go main.go
git add internal/ingest/ internal/webhook/ main.go
git commit -m "refactor(ingest): extract scoring pass from webhook into internal/ingest"
```

---

## Task 4: `internal/mergescan` — scan & score merged PRs

**Files:**
- Create: `internal/mergescan/mergescan.go`
- Create: `internal/mergescan/mergescan_test.go`

**Interfaces:**
- Consumes: `store.{GetMeta,SetMeta}` (Task 1), `github.SplitRepo` (Task 2), the `ingest.Ingester.ScorePR` shape (Task 3).
- Produces:
  - `type mergescan.Ingester interface { ScorePR(ctx context.Context, fullName, owner, repo string, number int) error }`
  - `type mergescan.Lister interface { FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error) }`
  - `func mergescan.New(lister Lister, ingester Ingester, st *store.Store, backfillDays int) *Scanner`
  - `func (s *Scanner) ScanRepo(ctx context.Context, repo string, now time.Time) error`

- [ ] **Step 1: Write the failing test**

Create `internal/mergescan/mergescan_test.go`:

```go
package mergescan

import (
	"context"
	"errors"
	"testing"
	"time"

	"pr-review-dashboard/internal/store"
)

type fakeLister struct {
	since   time.Time
	called  int
	numbers []int
}

func (f *fakeLister) FetchMergedPRNumbers(_ context.Context, _, _ string, since time.Time) ([]int, error) {
	f.called++
	f.since = since
	return f.numbers, nil
}

type fakeIngester struct {
	scored  []int
	failOn  int  // PR number to fail on; 0 = never
}

func (f *fakeIngester) ScorePR(_ context.Context, _, _, _ string, number int) error {
	if number == f.failOn {
		return errors.New("boom")
	}
	f.scored = append(f.scored, number)
	return nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestScanRepoBackfillWindowOnFirstRun(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{numbers: []int{7}}
	ig := &fakeIngester{}
	s := New(l, ig, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	want := now.AddDate(0, 0, -30)
	if !l.since.Equal(want) {
		t.Errorf("first-run since = %v, want %v (now-30d)", l.since, want)
	}
	if len(ig.scored) != 1 || ig.scored[0] != 7 {
		t.Errorf("scored = %v, want [7]", ig.scored)
	}
	// Mark advanced to now.
	v, found, _ := st.GetMeta("last_merge_scan:acme/widgets")
	if !found || v != now.UTC().Format(time.RFC3339) {
		t.Errorf("mark = %q found=%v, want %q", v, found, now.UTC().Format(time.RFC3339))
	}
}

func TestScanRepoIncrementalUsesMarkMinusOverlap(t *testing.T) {
	st := newStore(t)
	mark := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)
	st.SetMeta("last_merge_scan:acme/widgets", mark.Format(time.RFC3339))
	l := &fakeLister{}
	s := New(l, &fakeIngester{}, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	want := mark.Add(-time.Hour)
	if !l.since.Equal(want) {
		t.Errorf("incremental since = %v, want %v (mark-1h)", l.since, want)
	}
}

func TestScanRepoDoesNotAdvanceMarkOnError(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{numbers: []int{7, 8}}
	ig := &fakeIngester{failOn: 8}
	s := New(l, ig, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err == nil {
		t.Fatal("ScanRepo: want error from failing ScorePR, got nil")
	}
	if _, found, _ := st.GetMeta("last_merge_scan:acme/widgets"); found {
		t.Error("mark advanced despite a ScorePR error; window must retry next cycle")
	}
}

func TestScanRepoDisabled(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{}
	ig := &fakeIngester{}
	s := New(l, ig, st, 0)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if l.called != 0 || len(ig.scored) != 0 {
		t.Errorf("disabled scan touched lister(%d)/ingester(%d), want 0/0", l.called, len(ig.scored))
	}
}

func TestScanRepoBadRepo(t *testing.T) {
	st := newStore(t)
	s := New(&fakeLister{}, &fakeIngester{}, st, 30)
	if err := s.ScanRepo(context.Background(), "noslash", time.Now()); err == nil {
		t.Error("want error for malformed repo, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mergescan/ -v`
Expected: FAIL — package has no non-test files / `undefined: New`.

- [ ] **Step 3: Implement `internal/mergescan/mergescan.go`**

```go
// Package mergescan scans a repository for recently-merged PRs and scores them
// via an Ingester. The scan window starts at a per-repo high-water mark (or
// now-backfillDays on the first run), making the first scan a backfill.
package mergescan

import (
	"context"
	"fmt"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/store"
)

// scanOverlap is re-scanned before the high-water mark each cycle; idempotency
// makes the overlap free and guards against clock skew / boundary misses.
const scanOverlap = time.Hour

// Ingester scores a single PR (implemented by *ingest.Ingester).
type Ingester interface {
	ScorePR(ctx context.Context, fullName, owner, repo string, number int) error
}

// Lister lists merged PR numbers since a time (implemented by *github.Client).
type Lister interface {
	FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error)
}

// Scanner ingests merged PRs for a repo on each call, tracking progress with a
// per-repo high-water mark in the store.
type Scanner struct {
	lister       Lister
	ingester     Ingester
	st           *store.Store
	backfillDays int
}

// New constructs a Scanner. backfillDays <= 0 disables scanning entirely.
func New(lister Lister, ingester Ingester, st *store.Store, backfillDays int) *Scanner {
	return &Scanner{lister: lister, ingester: ingester, st: st, backfillDays: backfillDays}
}

// ScanRepo ingests PRs merged in repo ("owner/name") since the high-water mark
// (or now-backfillDays on the first run), then advances the mark on full
// success. A list or score error leaves the mark unadvanced so the window
// retries next cycle. No-op when backfillDays <= 0.
func (s *Scanner) ScanRepo(ctx context.Context, repo string, now time.Time) error {
	if s.backfillDays <= 0 {
		return nil
	}
	owner, name, ok := github.SplitRepo(repo)
	if !ok {
		return fmt.Errorf("bad repo %q, want owner/name", repo)
	}
	since, err := s.since(repo, now)
	if err != nil {
		return err
	}
	numbers, err := s.lister.FetchMergedPRNumbers(ctx, owner, name, since)
	if err != nil {
		return fmt.Errorf("list merged %s: %w", repo, err)
	}
	for _, n := range numbers {
		if err := s.ingester.ScorePR(ctx, repo, owner, name, n); err != nil {
			return fmt.Errorf("score %s#%d: %w", repo, n, err)
		}
	}
	return s.st.SetMeta(metaKey(repo), now.UTC().Format(time.RFC3339))
}

// since resolves the scan start: the stored mark minus the overlap, or
// now-backfillDays when there is no (or an unparseable) mark.
func (s *Scanner) since(repo string, now time.Time) (time.Time, error) {
	v, found, err := s.st.GetMeta(metaKey(repo))
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return now.AddDate(0, 0, -s.backfillDays), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return now.AddDate(0, 0, -s.backfillDays), nil // corrupt mark -> full window
	}
	return t.Add(-scanOverlap), nil
}

func metaKey(repo string) string { return "last_merge_scan:" + repo }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mergescan/ -v`
Expected: PASS — all five scan tests green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/mergescan/mergescan.go internal/mergescan/mergescan_test.go
git add internal/mergescan/
git commit -m "feat(mergescan): scan & score merged PRs with per-repo high-water mark"
```

---

## Task 5: Config — `BACKFILL_DAYS`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go` (append)
- Modify: `.env.example`

**Interfaces:**
- Produces (added to `config.Config`): `BackfillDays int`.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadBackfillDays(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")

	t.Setenv("BACKFILL_DAYS", "")
	if c, err := Load("does-not-exist.json"); err != nil || c.BackfillDays != 30 {
		t.Fatalf("default: BackfillDays=%d err=%v, want 30 nil", c.BackfillDays, err)
	}

	t.Setenv("BACKFILL_DAYS", "7")
	if c, _ := Load("does-not-exist.json"); c.BackfillDays != 7 {
		t.Errorf("set: BackfillDays=%d, want 7", c.BackfillDays)
	}

	t.Setenv("BACKFILL_DAYS", "0")
	if c, _ := Load("does-not-exist.json"); c.BackfillDays != 0 {
		t.Errorf("zero: BackfillDays=%d, want 0 (disabled)", c.BackfillDays)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadBackfillDays -v`
Expected: FAIL — `c.BackfillDays undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add the field to `Config` (after `WebhookSecret string`):

```go
	BackfillDays    int
```

In the `c := Config{...}` literal, add:

```go
		BackfillDays:    intOr("BACKFILL_DAYS", 30),
```

(`intOr` already exists from scoring-v2 and returns the parsed value when set, so `BACKFILL_DAYS=0` yields `0`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all config tests green.

- [ ] **Step 5: Update `.env.example`**

Append:

```
# Merge-scan backfill depth in days for a repo's first scan. 0 disables all merge-scanning.
BACKFILL_DAYS=30
```

- [ ] **Step 6: Commit**

```bash
gofumpt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go .env.example
git commit -m "feat(config): BACKFILL_DAYS (default 30, 0 disables)"
```

---

## Task 6: Wire scanner into the poll loop + docs

**Files:**
- Modify: `main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `mergescan.New`, `(*mergescan.Scanner).ScanRepo`, `cfg.BackfillDays`, the shared `gh` client + `ing` from Task 3.

- [ ] **Step 1: Build the scanner and call it in the poll loop**

In `main.go`, add import `"pr-review-dashboard/internal/mergescan"`. After the `ing := ingest.New(gh, st, cfg.Weights)` line (added in Task 3), construct the scanner:

```go
	scanner := mergescan.New(gh, ing, st, cfg.BackfillDays)
```

Inside the existing poll goroutine, the repo loop currently reads:

```go
				for _, repo := range cfg.Repos {
					if err := p.SyncRepo(ctx, repo); err != nil {
						log.Printf("repo sync %s: %v", repo, err)
					}
				}
```

Change it to also scan merges:

```go
				for _, repo := range cfg.Repos {
					if err := p.SyncRepo(ctx, repo); err != nil {
						log.Printf("repo sync %s: %v", repo, err)
					}
					if err := scanner.ScanRepo(ctx, repo, time.Now()); err != nil {
						log.Printf("merge scan %s: %v", repo, err)
					}
				}
```

Add a startup log line after constructing the scanner, mirroring the digest/webhook log style:

```go
	if cfg.BackfillDays > 0 {
		log.Printf("merge scan enabled: first run backfills %d days, then incremental", cfg.BackfillDays)
	} else {
		log.Print("merge scan disabled: set BACKFILL_DAYS > 0 to enable")
	}
```

- [ ] **Step 2: Build the whole binary**

Run: `go build ./...`
Expected: success. (`time` is already imported in `main.go`.)

- [ ] **Step 3: Full test suite + vet + format check**

Run: `go test ./... && go vet ./... && go run mvdan.cc/gofumpt@latest -l .`
Expected: all packages PASS; `go vet` clean; `gofumpt -l .` prints nothing.

- [ ] **Step 4: Update README**

In `README.md`, update the "How it works" section and scoring intro to state: scoring is now **poll-driven** — the poll loop scans merged PRs each cycle (first scan backfills `BACKFILL_DAYS` days, default 30; later scans ingest new merges), and the GitHub webhook (`POST /webhook/github`) is an **optional** alternate trigger enabled only when `WEBHOOK_SECRET` is set. Replace any text implying the webhook is the primary/only scoring path. Document `BACKFILL_DAYS` in the env table (default 30; `0` disables merge-scanning).

- [ ] **Step 5: Commit**

```bash
git add main.go README.md
git commit -m "feat(mergescan): wire scanner into poll loop; doc poll-driven scoring"
```

---

## Task 7: Manual smoke test

**Files:** none (verification only).

- [ ] **Step 1: Build a binary and run against qompass with a short backfill**

```bash
go build -o /tmp/lb-bin .
GITHUB_TOKEN=$(gh auth token) REPOS=Qumulo/qompass DB_PATH=/tmp/lb-mergescan.db \
HEALTH_PORT=8077 BACKFILL_DAYS=30 /tmp/lb-bin >/tmp/lb-mergescan.log 2>&1 &
SVR=$!; sleep 20   # allow one poll cycle + the backfill scan + per-PR fetches
curl -s "localhost:8077/api/leaderboard?window=all" | head -c 400; echo
kill -9 $SVR 2>/dev/null; lsof -ti :8077 | xargs kill -9 2>/dev/null
grep -i "merge scan" /tmp/lb-mergescan.log || true
```

Expected: the `merge scan enabled` log line is present; after the first cycle the leaderboard JSON shows non-zero `points` for reviewers of recently-merged qompass PRs (no longer all zero). Use a fresh `DB_PATH` so the first run performs the backfill.

- [ ] **Step 2: Confirm the disable switch**

```bash
GITHUB_TOKEN=$(gh auth token) REPOS=Qumulo/qompass DB_PATH=/tmp/lb-off.db \
HEALTH_PORT=8078 BACKFILL_DAYS=0 /tmp/lb-bin >/tmp/lb-off.log 2>&1 &
SVR=$!; sleep 5
grep -i "merge scan disabled" /tmp/lb-off.log
kill -9 $SVR 2>/dev/null; lsof -ti :8078 | xargs kill -9 2>/dev/null
```

Expected: `merge scan disabled` logged; no scanning occurs.

> NOTE: kill the spawned binary explicitly (and free the port) between runs — a `go run`/binary child can outlive a parent kill and answer stale on a reused port.

---

## Self-Review

**Spec coverage:**
- Poll-driven scoring on the existing loop → Task 6 wiring. ✅
- Backfill = first scan, no high-water mark → Task 4 `since` + Task 6. ✅
- `BACKFILL_DAYS` default 30, `0` disables → Task 5 + Task 4 `backfillDays <= 0` guard. ✅
- Per-repo high-water mark in `meta` table → Task 1 + Task 4. ✅
- 1-hour overlap → Task 4 `scanOverlap`. ✅
- `FetchMergedPRNumbers` window + stop-on-`updatedAt<since` → Task 2. ✅
- Single scoring path shared by webhook + scanner → Task 3 extraction; both call `ingest.ScorePR`. ✅
- Webhook retained, optional, slimmed → Task 3. ✅
- Idempotency via `raw_hash` → preserved in moved `ScorePR` (Task 3). ✅
- Errors leave mark unadvanced; no panic → Task 4. ✅
- One scoring path / no duplication; `SplitRepo` shared (no third copy) → Task 2 + Task 3 + Task 4. ✅

**Open verification points resolved:** OVP1 (stop-paging cutoff relies on `updatedAt >= mergedAt`; flagged in Task 2 query comment). OVP2 (extraction symbol move — Task 3 Step 4 enumerates removed symbols; Step 7 builds to catch dangling refs). OVP3 (`intOr` preserves `0` — Task 5 test asserts it).

**Placeholder scan:** none — every code step shows full code.

**Type consistency:** `ingest.New(fetcher, st, w)` / `ScorePR(ctx, fullName, owner, repo, number)` identical across ingest (Task 3), the `mergescan.Ingester` interface (Task 4), and `webhook` (Task 3). `mergescan.New(lister, ingester, st, backfillDays)` and `ScanRepo(ctx, repo, now)` consistent between Task 4 def and Task 6 call. `FetchMergedPRNumbers(ctx, owner, repo, since)` identical in Task 2 (def), the `mergescan.Lister` interface, and the `gh` client passed in Task 6. `GetMeta`/`SetMeta` signatures consistent across Tasks 1 and 4. `github.SplitRepo` consistent across Tasks 2, 3, 4.

**Behavioral note for the executor:** Task 3 is an extraction — `score`/`seedGuest`/`hashKey`/`PRFetcher` move out of `webhook.go` into `ingest.go` and the scoring-detail tests move from `webhook_test.go` to `ingest_test.go`. After it, `grep -rn "splitFullName\|h.score\|webhook.PRFetcher" internal/ main.go` should return nothing.
