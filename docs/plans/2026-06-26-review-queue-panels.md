# Review Queue Panels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the plain review queue with a dedicated Review-queue tab of urgency-tiered, clickable PR panels showing author, reviewer states (with re-request badges), size, and age/activity.

**Architecture:** Extend the open-PR snapshot with size + per-reviewer state (derived in the poller from the fetch it already makes), persist it on the `prs` row (scalar LOC columns + a `reviewers_json` blob). `store.Queue` returns enriched rows; a pure `store.RankQueue` assigns an urgency tier and sorts urgent-first; the `/api/queue` handler ranks with the configured stale threshold. A new Vue Review-queue tab renders the rows as themed panels.

**Tech Stack:** Go 1.25 stdlib only (`encoding/json`, `sort`, `time`), `modernc.org/sqlite`; Vue 3 + Vite (no new deps). Tests: stdlib `testing` + `httptest`; vitest for the frontend.

## Global Constraints

- **No new dependencies** — Go stdlib + existing `vue` only.
- **No new GitHub calls** — only extra fields (`additions deletions changedFiles`) on the existing open-PR query.
- **Storage = Approach A** — scalar LOC columns + a `reviewers_json` blob on the `prs` snapshot; no normalized reviewers table.
- **Awaiting rule (single source, in store):** `len(Reviewers) == 0` OR any reviewer status is `pending` or `commented`. The digest consumes `QueueRow.Awaiting` (its local `isAwaiting` is removed); digest output is unchanged.
- **Reviewer derivation:** a *currently-requested* reviewer is `pending` with `re_requested = true` iff they also have a prior review; a reviewer who reviewed but is not currently requested keeps their latest state (`approved`/`changes`/`commented`); the PR author is excluded.
- **Tiers (locked):** `reviewed` if not awaiting; else `new` if `AgeHours < 24`; else `urgent` if `AgeHours > STALE_PR_HOURS` (config, default 48); else `waiting`. Sort: `urgent < waiting < new < reviewed`, then `AgeHours` descending.
- **Navigation:** in-app tab toggle (no router). **Identity:** none — re-requests shown for everyone. **Click:** whole panel links to the PR (`target=_blank`).
- **Theme:** reuse the qompass-nexus tokens/chip styles already in `web/src/styles/theme.css`.
- **Errors:** never ignored; a row whose `reviewers_json` fails to parse yields an empty reviewer list (logged), not a failed request. No `panic`. `gofumpt`; exported types/funcs documented.

---

## File Structure

- `internal/github/client.go` — `prQuery` + `prGQL` + `FetchedPR` gain LOC fields. (Task 1)
- `internal/store/store.go` — `prs` schema/migration; `PR` fields; `UpsertPR` persists them; `QueueReviewer.ReRequested`. (Task 2)
- `internal/store/queries.go` — `QueueRow` fields; `Queue` parses `reviewers_json` + sets `Awaiting`/activity/size; new `RankQueue` + `newPRHours`; drop dead `reviewerStatus`. (Task 3)
- `internal/poller/poller.go` — `buildReviewers` + `lastActivity` helpers; `SyncRepo` populates the enriched snapshot. (Task 4)
- `internal/digest/digest.go` — `Run` uses `q.Awaiting`; remove `isAwaiting`. (Task 5)
- `internal/httpserver/server.go` + `main.go` — `New` gains `staleHours`; `/api/queue` ranks. (Task 6)
- `web/src/App.vue`, `web/src/components/Queue.vue`, `web/src/components/QueuePanel.vue` (new), `web/src/__tests__/queue.test.ts` (new) — tab + panels. (Task 7)
- build + screenshot. (Task 8)

---

## Task 1: GitHub — fetch PR size

**Files:**
- Modify: `internal/github/client.go`
- Test: `internal/github/client_test.go` (append)

**Interfaces:**
- Produces: `FetchedPR` gains `Additions, Deletions, ChangedFiles int`, populated from the open-PR list query.

- [ ] **Step 1: Write the failing test**

Append to `internal/github/client_test.go` (imports `context`, `net/http`, `net/http/httptest`, `testing` are present):

```go
func TestFetchPullRequestsParsesSize(t *testing.T) {
	const body = `{"data":{"repository":{"pullRequests":{
		"nodes":[{
			"number":7,"title":"t","url":"u","isDraft":false,
			"author":{"login":"alice"},
			"createdAt":"2026-06-20T10:00:00Z","updatedAt":"2026-06-21T10:00:00Z","mergedAt":null,
			"additions":210,"deletions":18,"changedFiles":4,
			"reviewRequests":{"nodes":[]},
			"reviews":{"nodes":[]}
		}],
		"pageInfo":{"hasNextPage":false,"endCursor":null}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	prs, err := c.FetchPullRequests(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(prs))
	}
	if prs[0].Additions != 210 || prs[0].Deletions != 18 || prs[0].ChangedFiles != 4 {
		t.Errorf("size = +%d -%d / %d files, want +210 -18 / 4", prs[0].Additions, prs[0].Deletions, prs[0].ChangedFiles)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/github/ -run TestFetchPullRequestsParsesSize -v`
Expected: FAIL — `prs[0].Additions undefined`.

- [ ] **Step 3: Implement**

In `internal/github/client.go`:

(a) Add to the `FetchedPR` struct (after `RequestedReviewers []string`):

```go
	Additions    int
	Deletions    int
	ChangedFiles int
```

(b) In `prQuery`, add the three fields to the node selection — change the line
`        number title url isDraft`
to:

```graphql
        number title url isDraft additions deletions changedFiles
```

(c) In the `prGQL` parse struct's node, add (after `IsDraft   bool   \`json:"isDraft"\``):

```go
						Additions    int `json:"additions"`
						Deletions    int `json:"deletions"`
						ChangedFiles int `json:"changedFiles"`
```

(d) In `FetchPullRequests`, when building each `FetchedPR p`, set them (after `p.Author = login(n.Author)`):

```go
				p.Additions = n.Additions
				p.Deletions = n.Deletions
				p.ChangedFiles = n.ChangedFiles
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/github/ -v`
Expected: PASS — new test green; existing client tests still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/github/client.go internal/github/client_test.go
git add internal/github/client.go internal/github/client_test.go
git commit -m "feat(github): fetch PR additions/deletions/changedFiles"
```

---

## Task 2: Store — snapshot columns + persist reviewers

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go` (append)

**Interfaces:**
- Consumes: `QueueReviewer` (defined in `queries.go`).
- Produces:
  - `QueueReviewer` gains `ReRequested bool` (`json:"re_requested"`).
  - `PR` gains `Additions, Deletions, ChangedFiles int`, `LastActivity time.Time`, `Reviewers []QueueReviewer`.
  - `UpsertPR` persists the five new columns (`reviewers_json` = `json.Marshal(p.Reviewers)`).

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestUpsertPRPersistsSizeAndReviewers(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	err = st.UpsertPR(PR{
		Repo: "acme/widgets", Number: 7, Title: "t", Author: "alice", URL: "u",
		IsDraft: false, ReadyAt: now.Add(-72 * time.Hour),
		Additions: 210, Deletions: 18, ChangedFiles: 4, LastActivity: now,
		Reviewers: []QueueReviewer{{Login: "bob", Status: "pending", ReRequested: true}},
	})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	rows, err := st.Queue(now)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Additions != 210 || r.Deletions != 18 || r.ChangedFiles != 4 {
		t.Errorf("size = +%d -%d / %d", r.Additions, r.Deletions, r.ChangedFiles)
	}
	if len(r.Reviewers) != 1 || r.Reviewers[0].Login != "bob" || !r.Reviewers[0].ReRequested {
		t.Errorf("reviewers = %+v, want bob re_requested", r.Reviewers)
	}
}
```

> NOTE: this test also exercises Task 3's `Queue` parsing. Implement Task 2's persistence first; the test fully passes once Task 3 lands. To keep Task 2 self-contained, in Step 4 run the column round-trip via a direct read (below) and let the full `Queue`-based assertion pass after Task 3.

For Task 2's own green gate, instead assert persistence with a direct column read appended to the same file:

```go
func TestUpsertPRWritesColumns(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Now()
	if err := st.UpsertPR(PR{
		Repo: "r", Number: 1, Title: "t", Author: "a", URL: "u",
		ReadyAt: now, Additions: 5, Deletions: 2, ChangedFiles: 1, LastActivity: now,
		Reviewers: []QueueReviewer{{Login: "b", Status: "approved"}},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var adds, dels, files int
	var revJSON string
	err := st.db.QueryRow(`SELECT additions, deletions, changed_files, reviewers_json FROM prs WHERE repo='r' AND pr_number=1`).
		Scan(&adds, &dels, &files, &revJSON)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if adds != 5 || dels != 2 || files != 1 {
		t.Errorf("cols = %d/%d/%d, want 5/2/1", adds, dels, files)
	}
	if !strings.Contains(revJSON, `"login":"b"`) {
		t.Errorf("reviewers_json = %q", revJSON)
	}
}
```

(Add `"strings"` to the test imports if not present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestUpsertPRWritesColumns -v`
Expected: FAIL — `unknown field Additions in struct literal of type store.PR` / no such column.

- [ ] **Step 3: Implement**

In `internal/store/store.go`:

(a) Add the columns to the `prs` table in the `schema` const (change the `requested_reviewers TEXT,` block to include the new columns before `last_synced TEXT`):

```sql
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
  last_synced TEXT,
  PRIMARY KEY (repo, pr_number)
);
```

(b) In `migrate`, add the five columns idempotently (after the existing `has_image` guard):

```go
	for _, col := range []struct{ name, ddl string }{
		{"additions", "ALTER TABLE prs ADD COLUMN additions INTEGER NOT NULL DEFAULT 0"},
		{"deletions", "ALTER TABLE prs ADD COLUMN deletions INTEGER NOT NULL DEFAULT 0"},
		{"changed_files", "ALTER TABLE prs ADD COLUMN changed_files INTEGER NOT NULL DEFAULT 0"},
		{"last_activity", "ALTER TABLE prs ADD COLUMN last_activity TEXT"},
		{"reviewers_json", "ALTER TABLE prs ADD COLUMN reviewers_json TEXT"},
	} {
		if !hasColumn(db, "prs", col.name) {
			if _, err := db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
```

(c) Add fields to the `PR` struct (after `RequestedReviewers []string`):

```go
	Additions    int
	Deletions    int
	ChangedFiles int
	LastActivity time.Time
	Reviewers    []QueueReviewer
```

(d) Add `"encoding/json"` to the `store.go` imports. Replace `UpsertPR` so it persists the new columns:

```go
// UpsertPR inserts or replaces a PR snapshot.
func (s *Store) UpsertPR(p PR) error {
	revJSON, err := json.Marshal(p.Reviewers)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO prs
  (repo, pr_number, title, author, url, is_draft, ready_at, merged_at, updated_at,
   requested_reviewers, additions, deletions, changed_files, last_activity, reviewers_json, last_synced)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, pr_number) DO UPDATE SET
  title=excluded.title, author=excluded.author, url=excluded.url,
  is_draft=excluded.is_draft, ready_at=excluded.ready_at, merged_at=excluded.merged_at,
  updated_at=excluded.updated_at, requested_reviewers=excluded.requested_reviewers,
  additions=excluded.additions, deletions=excluded.deletions, changed_files=excluded.changed_files,
  last_activity=excluded.last_activity, reviewers_json=excluded.reviewers_json,
  last_synced=excluded.last_synced`,
		p.Repo, p.Number, p.Title, p.Author, p.URL, boolToInt(p.IsDraft),
		tsOrEmpty(p.ReadyAt), tsOrEmpty(p.MergedAt), tsOrEmpty(p.UpdatedAt),
		strings.Join(p.RequestedReviewers, ","), p.Additions, p.Deletions, p.ChangedFiles,
		tsOrEmpty(p.LastActivity), string(revJSON), tsOrEmpty(time.Now()))
	return err
}
```

(e) Add `ReRequested` to `QueueReviewer` in `internal/store/queries.go`:

```go
type QueueReviewer struct {
	Login       string `json:"login"`
	Status      string `json:"status"`
	ReRequested bool   `json:"re_requested"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestUpsertPRWritesColumns -v`
Expected: PASS. (Existing store tests still green — `go test ./internal/store/`. `TestUpsertPRPersistsSizeAndReviewers` will pass after Task 3; it is fine for it to fail/await Task 3 now, OR move it into Task 3 — keep it in Task 3's commit if it does not yet pass.)

> EXECUTION NOTE: if `TestUpsertPRPersistsSizeAndReviewers` does not pass under Task 2 alone (Queue not yet enriched), leave it as Task 3's test — move that function into Task 3 Step 1 rather than committing a red test here.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/store/store.go internal/store/store_test.go internal/store/queries.go
git add internal/store/store.go internal/store/store_test.go internal/store/queries.go
git commit -m "feat(store): prs size/last_activity/reviewers_json columns + persist"
```

---

## Task 3: Store — enriched Queue + RankQueue

**Files:**
- Modify: `internal/store/queries.go`
- Test: `internal/store/queries_test.go` (append)

**Interfaces:**
- Consumes: `QueueReviewer` (incl. `ReRequested`), `PR` (incl. size/`LastActivity`/`Reviewers`), `UpsertPR`.
- Produces:
  - `QueueRow` gains `LastActivityHours float64`, `Additions, Deletions, ChangedFiles int`, `Awaiting bool`, `Tier string`.
  - `Queue(now)` (unchanged signature) parses `reviewers_json`, computes `AgeHours`, `LastActivityHours`, size, and `Awaiting`.
  - `const newPRHours = 24` and `func RankQueue(rows []QueueRow, staleHours float64) []QueueRow`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/queries_test.go`:

```go
func TestQueueComputesAwaitingAndSize(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	if err := st.UpsertPR(store.PR{
		Repo: "acme/widgets", Number: 7, Title: "t", Author: "alice", URL: "u",
		ReadyAt: now.Add(-72 * time.Hour), LastActivity: now.Add(-2 * time.Hour),
		Additions: 210, Deletions: 18, ChangedFiles: 4,
		Reviewers: []store.QueueReviewer{
			{Login: "bob", Status: "pending", ReRequested: true},
			{Login: "carol", Status: "approved"},
		},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := st.Queue(now)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	r := rows[0]
	if r.Additions != 210 || r.Deletions != 18 || r.ChangedFiles != 4 {
		t.Errorf("size = +%d -%d /%d", r.Additions, r.Deletions, r.ChangedFiles)
	}
	if len(r.Reviewers) != 2 || !r.Reviewers[0].ReRequested {
		t.Errorf("reviewers = %+v", r.Reviewers)
	}
	if !r.Awaiting {
		t.Errorf("Awaiting = false, want true (bob pending)")
	}
	if r.LastActivityHours < 1.5 || r.LastActivityHours > 2.5 {
		t.Errorf("LastActivityHours = %v, want ~2", r.LastActivityHours)
	}
}

func TestQueueAwaitingRule(t *testing.T) {
	cases := []struct {
		name      string
		reviewers []store.QueueReviewer
		want      bool
	}{
		{"none", nil, true},
		{"a pending", []store.QueueReviewer{{Login: "a", Status: "pending"}}, true},
		{"commented only", []store.QueueReviewer{{Login: "a", Status: "commented"}}, true},
		{"all approved", []store.QueueReviewer{{Login: "a", Status: "approved"}}, false},
		{"changes", []store.QueueReviewer{{Login: "a", Status: "changes"}}, false},
		{"approved+pending", []store.QueueReviewer{{Login: "a", Status: "approved"}, {Login: "b", Status: "pending"}}, true},
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	for i, c := range cases {
		st.UpsertPR(store.PR{Repo: "r", Number: i + 1, Author: "x", ReadyAt: now, Reviewers: c.reviewers})
	}
	rows, _ := st.Queue(now)
	got := map[int]bool{}
	for _, r := range rows {
		got[r.PRNumber] = r.Awaiting
	}
	for i, c := range cases {
		if got[i+1] != c.want {
			t.Errorf("%s: Awaiting = %v, want %v", c.name, got[i+1], c.want)
		}
	}
}

func TestRankQueueTiersAndOrder(t *testing.T) {
	rows := []store.QueueRow{
		{PRNumber: 1, AgeHours: 100, Awaiting: true},  // urgent (>48)
		{PRNumber: 2, AgeHours: 30, Awaiting: true},   // waiting (24..48)
		{PRNumber: 3, AgeHours: 5, Awaiting: true},    // new (<24)
		{PRNumber: 4, AgeHours: 200, Awaiting: false}, // reviewed (not awaiting)
		{PRNumber: 5, AgeHours: 80, Awaiting: true},   // urgent, older than #1? no — younger
	}
	out := store.RankQueue(rows, 48)
	tier := map[int]string{}
	var order []int
	for _, r := range out {
		tier[r.PRNumber] = r.Tier
		order = append(order, r.PRNumber)
	}
	if tier[1] != "urgent" || tier[2] != "waiting" || tier[3] != "new" || tier[4] != "reviewed" || tier[5] != "urgent" {
		t.Fatalf("tiers = %v", tier)
	}
	// urgent first, oldest-first within tier: 1(100),5(80), then 2, then 3, then 4
	want := []int{1, 5, 2, 3, 4}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order = %v, want %v", order, want)
			break
		}
	}
}
```

(Move `TestUpsertPRPersistsSizeAndReviewers` here from Task 2 if it was left pending.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run 'TestQueue|TestRankQueue' -v`
Expected: FAIL — `r.Awaiting undefined` / `undefined: store.RankQueue`.

- [ ] **Step 3: Implement**

In `internal/store/queries.go`:

(a) Extend `QueueRow` (add fields; keep existing ones + json tags):

```go
type QueueRow struct {
	Repo              string          `json:"repo"`
	PRNumber          int             `json:"pr_number"`
	Title             string          `json:"title"`
	Author            string          `json:"author"`
	URL               string          `json:"url"`
	AgeHours          float64         `json:"age_hours"`
	LastActivityHours float64         `json:"last_activity_hours"`
	Additions         int             `json:"additions"`
	Deletions         int             `json:"deletions"`
	ChangedFiles      int             `json:"changed_files"`
	Awaiting          bool            `json:"awaiting"`
	Tier              string          `json:"tier"`
	Reviewers         []QueueReviewer `json:"reviewers"`
}
```

(b) Add `"encoding/json"` and `"log"` to the `queries.go` imports. Replace the `prSeed` struct + the `Queue` method so it reads the new columns and parses `reviewers_json` (drop the `reviewerStatus` reviewer-building):

```go
type prSeed struct {
	repo, title, author, url string
	prNumber                 int
	readyAt, lastActivity    string
	additions, deletions     int
	changedFiles             int
	reviewersJSON            string
}

// Queue returns open, non-draft, unmerged PRs enriched with size, activity, and
// reviewer state, newest-ready first. Tier/sort is applied by RankQueue.
func (s *Store) Queue(now time.Time) ([]QueueRow, error) {
	rows, err := s.db.Query(`
SELECT repo, pr_number, title, author, url, ready_at, last_activity,
       additions, deletions, changed_files, reviewers_json
FROM prs
WHERE is_draft = 0 AND merged_at = ''
ORDER BY ready_at DESC`)
	if err != nil {
		return nil, err
	}
	var seeds []prSeed
	for rows.Next() {
		var p prSeed
		var lastActivity, reviewersJSON sql.NullString
		if err := rows.Scan(&p.repo, &p.prNumber, &p.title, &p.author, &p.url, &p.readyAt,
			&lastActivity, &p.additions, &p.deletions, &p.changedFiles, &reviewersJSON); err != nil {
			rows.Close()
			return nil, err
		}
		p.lastActivity = lastActivity.String
		p.reviewersJSON = reviewersJSON.String
		seeds = append(seeds, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]QueueRow, 0, len(seeds))
	for _, p := range seeds {
		q := QueueRow{
			Repo: p.repo, PRNumber: p.prNumber, Title: p.title, Author: p.author, URL: p.url,
			Additions: p.additions, Deletions: p.deletions, ChangedFiles: p.changedFiles,
		}
		if t, err := time.Parse(time.RFC3339, p.readyAt); err == nil {
			q.AgeHours = now.Sub(t).Hours()
		}
		act := p.lastActivity
		if act == "" {
			act = p.readyAt
		}
		if t, err := time.Parse(time.RFC3339, act); err == nil {
			q.LastActivityHours = now.Sub(t).Hours()
		}
		if p.reviewersJSON != "" {
			if err := json.Unmarshal([]byte(p.reviewersJSON), &q.Reviewers); err != nil {
				log.Printf("queue: bad reviewers_json for %s#%d: %v", q.Repo, q.PRNumber, err)
				q.Reviewers = nil
			}
		}
		q.Awaiting = awaiting(q.Reviewers)
		out = append(out, q)
	}
	return out, nil
}

// awaiting reports whether a PR still needs review: no reviewers, or any
// reviewer is still pending or has only commented.
func awaiting(reviewers []QueueReviewer) bool {
	if len(reviewers) == 0 {
		return true
	}
	for _, rv := range reviewers {
		if rv.Status == "pending" || rv.Status == "commented" {
			return true
		}
	}
	return false
}

// newPRHours is the age below which a PR is treated as "new".
const newPRHours = 24

// RankQueue assigns each row's Tier and returns the rows sorted urgent-first
// (urgent < waiting < new < reviewed), then by AgeHours descending within a tier.
func RankQueue(rows []QueueRow, staleHours float64) []QueueRow {
	rank := map[string]int{"urgent": 0, "waiting": 1, "new": 2, "reviewed": 3}
	out := make([]QueueRow, len(rows))
	copy(out, rows)
	for i := range out {
		switch {
		case !out[i].Awaiting:
			out[i].Tier = "reviewed"
		case out[i].AgeHours < newPRHours:
			out[i].Tier = "new"
		case out[i].AgeHours > staleHours:
			out[i].Tier = "urgent"
		default:
			out[i].Tier = "waiting"
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := rank[out[a].Tier], rank[out[b].Tier]
		if ra != rb {
			return ra < rb
		}
		return out[a].AgeHours > out[b].AgeHours
	})
	return out
}
```

(c) Delete the now-unused `reviewerStatus` method (it built reviewer status from `review_events`, which is empty for open PRs in v2; reviewers now come from `reviewers_json`). Confirm with `grep -n reviewerStatus internal/store/` → no other callers. Keep `DistinctReviewers` (used by the poller's roster sync). `splitNonEmpty` may become unused — remove it too if `grep -n splitNonEmpty internal/store/` shows no remaining caller.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS — new queue/rank tests green; existing store tests still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/store/queries.go internal/store/queries_test.go
git add internal/store/queries.go internal/store/queries_test.go internal/store/store_test.go
git commit -m "feat(store): enriched Queue (awaiting/size/activity) + RankQueue tiers"
```

---

## Task 4: Poller — derive reviewers + size into the snapshot

**Files:**
- Modify: `internal/poller/poller.go`
- Test: `internal/poller/poller_test.go` (append)

**Interfaces:**
- Consumes: `github.FetchedPR` (incl. size + `Reviews`), `store.PR` (incl. size/`LastActivity`/`Reviewers`), `store.QueueReviewer`.
- Produces: `buildReviewers(fp github.FetchedPR) []store.QueueReviewer` and `lastActivity(fp github.FetchedPR) time.Time`; `SyncRepo` populates the enriched snapshot.

- [ ] **Step 1: Write the failing test**

Append to `internal/poller/poller_test.go` (imports `time`, `github`, `store` present):

```go
func TestBuildReviewers(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	fp := github.FetchedPR{
		Author:             "alice",
		RequestedReviewers: []string{"bob", "carol"}, // bob re-requested (has prior review), carol fresh request
		Reviews: []github.FetchedReview{
			{Author: "bob", State: "APPROVED", SubmittedAt: base},
			{Author: "dave", State: "COMMENTED", SubmittedAt: base},
			{Author: "dave", State: "CHANGES_REQUESTED", SubmittedAt: base.Add(time.Hour)}, // latest wins
			{Author: "alice", State: "APPROVED", SubmittedAt: base},                        // self — excluded
		},
	}
	got := buildReviewers(fp)
	by := map[string]store.QueueReviewer{}
	for _, r := range got {
		by[r.Login] = r
	}
	if len(got) != 3 {
		t.Fatalf("got %d reviewers, want 3 (bob, carol, dave): %+v", len(got), got)
	}
	if by["bob"].Status != "pending" || !by["bob"].ReRequested {
		t.Errorf("bob = %+v, want pending + re_requested", by["bob"])
	}
	if by["carol"].Status != "pending" || by["carol"].ReRequested {
		t.Errorf("carol = %+v, want pending, not re_requested", by["carol"])
	}
	if by["dave"].Status != "changes" || by["dave"].ReRequested {
		t.Errorf("dave = %+v, want changes (latest), not re_requested", by["dave"])
	}
	if _, ok := by["alice"]; ok {
		t.Errorf("PR author alice must be excluded")
	}
}

func TestLastActivity(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	fp := github.FetchedPR{
		UpdatedAt: base,
		Reviews:   []github.FetchedReview{{Author: "b", State: "COMMENTED", SubmittedAt: base.Add(3 * time.Hour)}},
	}
	if got := lastActivity(fp); !got.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("lastActivity = %v, want %v (latest review)", got, base.Add(3*time.Hour))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/poller/ -run 'TestBuildReviewers|TestLastActivity' -v`
Expected: FAIL — `undefined: buildReviewers` / `undefined: lastActivity`.

- [ ] **Step 3: Implement**

In `internal/poller/poller.go`, add `"sort"` and `"time"` to imports (if not present — `time` likely is via signatures; add `sort`). Add the helpers and wire `SyncRepo`.

```go
// mapReviewState maps a GitHub review state to the queue's status vocabulary.
func mapReviewState(s string) string {
	switch s {
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

// buildReviewers derives per-reviewer status for an open PR. A currently
// requested reviewer is pending (they owe a review now) and flagged
// re_requested if they have a prior review; a reviewer who reviewed but is not
// currently requested keeps their latest state. The PR author is excluded.
func buildReviewers(fp github.FetchedPR) []store.QueueReviewer {
	type rev struct {
		state string
		at    time.Time
	}
	latest := map[string]rev{}
	for _, r := range fp.Reviews {
		if r.Author == "" || r.Author == fp.Author {
			continue
		}
		if cur, ok := latest[r.Author]; !ok || r.SubmittedAt.After(cur.at) {
			latest[r.Author] = rev{state: r.State, at: r.SubmittedAt}
		}
	}
	requested := map[string]bool{}
	var reqList []string
	for _, l := range fp.RequestedReviewers {
		if l == "" || l == fp.Author || requested[l] {
			continue
		}
		requested[l] = true
		reqList = append(reqList, l)
	}
	var revList []string
	for l := range latest {
		if !requested[l] {
			revList = append(revList, l)
		}
	}
	sort.Strings(reqList)
	sort.Strings(revList)

	out := make([]store.QueueReviewer, 0, len(reqList)+len(revList))
	for _, l := range reqList {
		_, reviewed := latest[l]
		out = append(out, store.QueueReviewer{Login: l, Status: "pending", ReRequested: reviewed})
	}
	for _, l := range revList {
		out = append(out, store.QueueReviewer{Login: l, Status: mapReviewState(latest[l].state)})
	}
	return out
}

// lastActivity is the most recent of the PR's updatedAt and any review time.
func lastActivity(fp github.FetchedPR) time.Time {
	t := fp.UpdatedAt
	for _, r := range fp.Reviews {
		if r.SubmittedAt.After(t) {
			t = r.SubmittedAt
		}
	}
	return t
}
```

In `SyncRepo`, extend the `store.PR` built from each `fp` (add the new fields):

```go
		if err := p.st.UpsertPR(store.PR{
			Repo: repo, Number: fp.Number, Title: fp.Title, Author: fp.Author, URL: fp.URL,
			IsDraft: fp.IsDraft, ReadyAt: fp.ReadyAt, MergedAt: fp.MergedAt, UpdatedAt: fp.UpdatedAt,
			RequestedReviewers: fp.RequestedReviewers,
			Additions:          fp.Additions,
			Deletions:          fp.Deletions,
			ChangedFiles:       fp.ChangedFiles,
			LastActivity:       lastActivity(fp),
			Reviewers:          buildReviewers(fp),
		}); err != nil {
			return err
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/poller/ -v`
Expected: PASS — helper tests green; existing poller tests (`TestSyncRepoSnapshotsButDoesNotScore`, `TestSyncRosterMarksGuests`) still green.

- [ ] **Step 5: Commit**

```bash
gofumpt -w internal/poller/poller.go internal/poller/poller_test.go
git add internal/poller/poller.go internal/poller/poller_test.go
git commit -m "feat(poller): derive reviewer states + PR size into the queue snapshot"
```

---

## Task 5: Digest — consume `QueueRow.Awaiting`

**Files:**
- Modify: `internal/digest/digest.go`
- Modify: `internal/digest/digest_test.go`

**Interfaces:**
- Consumes: `store.QueueRow.Awaiting` (Task 3).

- [ ] **Step 1: Update the stale filter + remove `isAwaiting`**

In `internal/digest/digest.go`, the `Run` stale loop currently reads:

```go
		if q.AgeHours > d.staleHours && isAwaiting(q) {
```

Change it to use the store-computed field:

```go
		if q.AgeHours > d.staleHours && q.Awaiting {
```

Delete the `isAwaiting` function entirely (the rule now lives in `store.awaiting`, set on every `QueueRow`).

- [ ] **Step 2: Remove the now-orphaned test**

In `internal/digest/digest_test.go`, delete `TestIsAwaiting` (it tested the removed helper; the rule is covered by `store.TestQueueAwaitingRule`). Update any `store.QueueRow{...}` literals used by other digest tests to set `Awaiting: true` where they previously relied on `isAwaiting` deriving it (e.g. `TestRunPostsDigest`'s stale PR seed: the PR is seeded via `UpsertPR`, so `Queue` computes `Awaiting` — verify those tests seed reviewers such that `Awaiting` is true; a PR with no reviewers is awaiting, which matches the existing seeds).

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS — digest builds without `isAwaiting`; `TestRunPostsDigest` / `TestRunAllCaughtUp` still green (a no-reviewer stale PR is awaiting; an all-caught-up DB yields no stale rows).
Expected failure if missed: `undefined: isAwaiting` (a literal still references it) — fix the reference.

- [ ] **Step 4: Commit**

```bash
gofumpt -w internal/digest/digest.go internal/digest/digest_test.go
git add internal/digest/digest.go internal/digest/digest_test.go
git commit -m "refactor(digest): use store QueueRow.Awaiting; drop local isAwaiting"
```

---

## Task 6: HTTP server — rank the queue with the stale threshold

**Files:**
- Modify: `internal/httpserver/server.go`
- Modify: `internal/httpserver/server_test.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `store.RankQueue`, `store.Queue`, `cfg.StalePRHours`.
- Produces: `httpserver.New` gains a trailing `staleHours float64` param; `/api/queue` returns `RankQueue(Queue(now), staleHours)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpserver/server_test.go`:

```go
func TestQueueEndpointRanksUrgentFirst(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	// An urgent PR (awaiting, 100h) and a new PR (awaiting, 5h).
	st.UpsertPR(store.PR{Repo: "r", Number: 1, Author: "a", URL: "u1", ReadyAt: now.Add(-100 * time.Hour),
		Reviewers: []store.QueueReviewer{{Login: "x", Status: "pending"}}})
	st.UpsertPR(store.PR{Repo: "r", Number: 2, Author: "a", URL: "u2", ReadyAt: now.Add(-5 * time.Hour),
		Reviewers: []store.QueueReviewer{{Login: "x", Status: "pending"}}})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48)
	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.QueueRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 || rows[0].PRNumber != 1 || rows[0].Tier != "urgent" || rows[1].Tier != "new" {
		t.Errorf("rows = %+v, want #1 urgent first then #2 new", rows)
	}
}
```

Update the **existing** `New(...)` call sites in `server_test.go` to pass a fifth arg `48` (any positive float): the calls in `TestLeaderboardEndpoint`, `TestHealthEndpoint`, `TestDigestRunTrigger`, `TestDigestRunDisabled`, `TestDigestRunRejectsGET`, `TestWebhookRouteMounted`, `TestWebhookRouteDisabled`. (`json` and `time` are already imported in this test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpserver/ -v`
Expected: FAIL — `not enough arguments in call to New`.

- [ ] **Step 3: Implement**

In `internal/httpserver/server.go`, change the signature and the `/api/queue` handler:

```go
func New(st *store.Store, assets fs.FS, runDigest func(context.Context) error, webhook http.Handler, staleHours float64) http.Handler {
```

```go
	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.Queue(time.Now())
		if err == nil {
			rows = store.RankQueue(rows, staleHours)
		}
		writeJSON(w, rows, err)
	})
```

- [ ] **Step 4: Wire `main.go`**

In `main.go`, pass `cfg.StalePRHours` as the new fifth arg:

```go
	h := httpserver.New(st, httpserver.Assets(), runDigest, webhookHandler, cfg.StalePRHours)
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/httpserver/ -v && go build ./...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
gofumpt -w internal/httpserver/server.go internal/httpserver/server_test.go main.go
git add internal/httpserver/server.go internal/httpserver/server_test.go main.go
git commit -m "feat(httpserver): rank /api/queue urgent-first with STALE_PR_HOURS"
```

---

## Task 7: Frontend — Review-queue tab + PR panels

**Files:**
- Modify: `web/src/App.vue`
- Modify: `web/src/components/Queue.vue`
- Create: `web/src/components/QueuePanel.vue`
- Create: `web/src/__tests__/queue.test.ts`

**Interfaces:**
- Consumes: `/api/queue` rows (ranked; each has `tier`, `reviewers[].re_requested`, `additions`, `deletions`, `changed_files`, `age_hours`, `last_activity_hours`).

- [ ] **Step 1: Write the failing component test**

Create `web/src/__tests__/queue.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import QueuePanel from '../components/QueuePanel.vue'

const base = {
  repo: 'acme/widgets', pr_number: 7, title: 'Add fleet sync', author: 'alice',
  url: 'https://gh/7', age_hours: 100, last_activity_hours: 2,
  additions: 210, deletions: 18, changed_files: 4, awaiting: true,
  tier: 'urgent', reviewers: [{ login: 'bob', status: 'pending', re_requested: true }],
}

describe('QueuePanel', () => {
  it('renders the PR ref, links to the PR, and tiers by urgency', () => {
    const w = mount(QueuePanel, { props: { pr: base } })
    expect(w.text()).toContain('acme/widgets#7')
    expect(w.get('a').attributes('href')).toBe('https://gh/7')
    expect(w.get('a').classes()).toContain('tier-urgent')
  })
  it('shows a re-requested badge and +/- size', () => {
    const w = mount(QueuePanel, { props: { pr: base } })
    expect(w.text()).toMatch(/re-?requested/i)
    expect(w.text()).toContain('+210')
    expect(w.text()).toContain('−18')
  })
  it('shows a NEW chip only for the new tier', () => {
    expect(mount(QueuePanel, { props: { pr: { ...base, tier: 'new' } } }).text()).toContain('NEW')
    expect(mount(QueuePanel, { props: { pr: base } }).text()).not.toContain('NEW')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test`
Expected: FAIL — cannot resolve `../components/QueuePanel.vue`.

- [ ] **Step 3: Create `web/src/components/QueuePanel.vue`**

```vue
<script setup lang="ts">
defineProps<{
  pr: {
    repo: string; pr_number: number; title: string; author: string; url: string
    age_hours: number; last_activity_hours: number
    additions: number; deletions: number; changed_files: number
    awaiting: boolean; tier: string
    reviewers: Array<{ login: string; status: string; re_requested: boolean }>
  }
}>()

const STATUS: Record<string, { icon: string; cls: string }> = {
  approved: { icon: '✓', cls: 'ok' },
  changes: { icon: '±', cls: 'err' },
  commented: { icon: '💬', cls: 'comment' },
  pending: { icon: '○', cls: 'pending' },
}
const sIcon = (s: string) => (STATUS[s] ?? STATUS.pending).icon
const sCls = (s: string) => (STATUS[s] ?? STATUS.pending).cls
const hrs = (h: number) => (h >= 48 ? `${Math.round(h / 24)}d` : `${Math.round(h)}h`)
</script>

<template>
  <a class="panel" :class="'tier-' + pr.tier" :href="pr.url" target="_blank" rel="noopener">
    <div class="panel__head">
      <span class="ref">{{ pr.repo }}#{{ pr.pr_number }}</span>
      <span class="title">{{ pr.title }}</span>
      <span v-if="pr.tier === 'new'" class="chip chip--new">NEW</span>
      <span class="open">↗</span>
    </div>
    <div class="panel__meta">
      <span class="by">{{ pr.author }}</span>
      <span class="loc"><span class="add">+{{ pr.additions }}</span> <span class="del">−{{ pr.deletions }}</span> · {{ pr.changed_files }} files</span>
      <span class="age">{{ hrs(pr.age_hours) }} old</span>
      <span class="act">active {{ hrs(pr.last_activity_hours) }} ago</span>
    </div>
    <div class="panel__rev">
      <span v-for="rv in pr.reviewers" :key="rv.login" class="chip" :class="'chip--' + sCls(rv.status)">
        <span class="chip__icon">{{ sIcon(rv.status) }}</span>{{ rv.login }}
        <span v-if="rv.re_requested" class="rr">re-requested</span>
      </span>
      <span v-if="pr.reviewers.length === 0" class="no-rev">no reviewers requested</span>
    </div>
  </a>
</template>

<style scoped>
.panel {
  display: block;
  color: inherit;
  text-decoration: none;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--border);
  border-radius: var(--radius-lg);
  padding: var(--space-s) var(--space-m);
  transition:
    transform var(--motion-fast) var(--motion-ease),
    box-shadow var(--motion-fast) var(--motion-ease),
    border-color var(--motion-fast) var(--motion-ease);
}
.panel:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow);
  border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
  text-decoration: none;
}
.tier-urgent {
  border-left-color: var(--err);
  background: color-mix(in srgb, var(--err) 5%, var(--bg-card));
}
.tier-waiting {
  border-left-color: var(--warn);
}
.tier-new {
  border-left-color: var(--accent);
}
.tier-reviewed {
  border-left-color: var(--ok);
}

.panel__head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2xs);
  flex-wrap: wrap;
}
.ref {
  font-family: var(--font-mono);
  font-weight: 600;
  color: var(--accent);
  white-space: nowrap;
}
.title {
  color: var(--fg-strong);
  font-weight: 500;
}
.open {
  margin-left: auto;
  color: var(--fg-subtle);
}

.panel__meta {
  display: flex;
  gap: var(--space-s);
  flex-wrap: wrap;
  margin-top: var(--space-2xs);
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
}
.by::before {
  content: 'by ';
}
.add {
  color: var(--ok);
}
.del {
  color: var(--err);
}

.panel__rev {
  display: flex;
  gap: var(--space-3xs);
  flex-wrap: wrap;
  margin-top: var(--space-xs);
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  color: var(--fg-subtle);
  background: var(--bg-sub);
  border: 1px solid var(--border-subtle);
}
.chip--ok {
  color: var(--ok);
  background: var(--ok-bg);
  border-color: color-mix(in srgb, var(--ok) 30%, transparent);
}
.chip--err {
  color: var(--err);
  background: var(--err-bg);
  border-color: color-mix(in srgb, var(--err) 30%, transparent);
}
.chip--comment {
  color: var(--tone-comment);
  background: color-mix(in srgb, var(--tone-comment) 14%, transparent);
  border-color: color-mix(in srgb, var(--tone-comment) 30%, transparent);
}
.chip--new {
  margin-left: var(--space-2xs);
  color: var(--accent);
  background: var(--accent-bg);
  border: 1px solid color-mix(in srgb, var(--accent) 30%, transparent);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.05em;
}
.rr {
  margin-left: 4px;
  color: var(--warn);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.03em;
}
.no-rev {
  color: var(--fg-subtle);
  font-size: var(--step--2);
}
</style>
```

- [ ] **Step 4: Rewrite `web/src/components/Queue.vue` as a panel grid**

```vue
<script setup lang="ts">
import QueuePanel from './QueuePanel.vue'
defineProps<{ rows: any[] }>()
</script>

<template>
  <div class="grid">
    <QueuePanel v-for="p in rows" :key="p.repo + p.pr_number" :pr="p" />
    <p v-if="rows.length === 0" class="empty">✅ Nothing waiting — the queue is clear.</p>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--space-s);
}
@media (min-width: 48rem) {
  .grid {
    grid-template-columns: 1fr 1fr;
  }
}
.empty {
  grid-column: 1 / -1;
  padding: var(--space-l);
  text-align: center;
  color: var(--fg-subtle);
}
</style>
```

- [ ] **Step 5: Add the view toggle to `web/src/App.vue`**

Add a `view` ref and a top-level segmented toggle; render the leaderboard (with its window tabs) under `view === 'leaderboard'` and the queue under `view === 'queue'`. In `<script setup>` add:

```ts
const view = ref<'leaderboard' | 'queue'>('leaderboard')
```

In the template, add a primary toggle directly under the masthead title block (reuse the `.seg` styles already in this file), and wrap the existing leaderboard section and the queue section in `v-if`s:

```html
      <div class="seg" role="tablist" aria-label="View">
        <button role="tab" :aria-selected="view === 'leaderboard'"
          :class="{ seg__opt: true, 'seg__opt--on': view === 'leaderboard' }"
          @click="view = 'leaderboard'">Leaderboard</button>
        <button role="tab" :aria-selected="view === 'queue'"
          :class="{ seg__opt: true, 'seg__opt--on': view === 'queue' }"
          @click="view = 'queue'">Review queue</button>
      </div>
```

Move the existing week/month/all `.seg` control to render only inside the leaderboard section. Wrap sections:

```html
    <template v-if="view === 'leaderboard'">
      <!-- window seg control + Top reviewers card (existing markup) -->
    </template>
    <section v-else class="queue-view">
      <div class="card__head bare">
        <h2>Ready for review</h2>
        <span class="card__meta">{{ queue.length }} open</span>
      </div>
      <Queue :rows="queue" />
    </section>
```

(Keep the masthead, error line, and footer outside the `v-if`. The queue view places its header above the panel grid; the panels are their own cards, so the queue section is not itself wrapped in a `.card`.)

- [ ] **Step 6: Run the frontend tests**

Run: `cd web && npm test`
Expected: PASS — `queue.test.ts` (3) green; the existing `leaderboard.test.ts` still green.

- [ ] **Step 7: Commit**

```bash
cd web && npm run build
cd ..
gofumpt -l . >/dev/null   # no Go files changed here; build step refreshes embedded assets
git add web/src/ internal/httpserver/web/
git commit -m "feat(ui): Review-queue tab with urgency-tiered PR panels"
```

---

## Task 8: Full gate + visual verification

**Files:** none (verification only).

- [ ] **Step 1: Full backend gate**

Run: `go build ./... && go test ./... && go vet ./... && go run mvdan.cc/gofumpt@latest -l .`
Expected: build OK; all packages pass; vet clean; `gofumpt -l .` prints nothing.

- [ ] **Step 2: Frontend gate**

Run: `cd web && npm test && npm run build`
Expected: vitest green; build writes `../internal/httpserver/web`.

- [ ] **Step 3: Rebuild binary + smoke against the qompass snapshot**

```bash
go build -o /tmp/lb-bin .
lsof -ti :8090 | xargs kill -9 2>/dev/null
WEBHOOK_SECRET= GITHUB_TOKEN=$(gh auth token) REPOS=Qumulo/qompass ROSTER_TEAM=Qumulo/qork \
  DB_PATH=/tmp/lb-p4-smoke.db HEALTH_PORT=8090 BACKFILL_DAYS=0 /tmp/lb-bin >/tmp/lb-q.log 2>&1 &
sleep 2
curl -s "localhost:8090/api/queue" | head -c 400; echo
```

Expected: ranked JSON with `tier` fields. (The snapshot DB's PRs were captured before this feature, so `reviewers_json`/size may be empty until a live poll repopulates them; run one live poll — omit `BACKFILL_DAYS=0` and a fresh `DB_PATH` — if you want fully populated panels.)

- [ ] **Step 4: Screenshot the Review-queue tab (Playwright)**

Navigate to `http://localhost:8090`, click the **Review queue** toggle, screenshot, and confirm: panels render, urgent panels (red left-border) sort first, NEW chips on new PRs, re-requested badges present, reviewer chips tinted. Kill the binary + free the port afterward.

> NOTE: kill the spawned binary explicitly and free the port between runs — a binary child can outlive a parent kill and answer stale on a reused port.

---

## Self-Review

**Spec coverage:**
- PR size fetched → Task 1. ✅
- Snapshot columns + reviewers blob + persist → Task 2. ✅
- Enriched `Queue` + `Awaiting` + `RankQueue` tiers/sort → Task 3. ✅
- Reviewer derivation (latest-per-author, re-request, author-excluded) + last-activity in poller → Task 4. ✅
- Digest consolidated onto `Awaiting` → Task 5. ✅
- `/api/queue` ranked with `STALE_PR_HOURS`; `New` param threaded → Task 6. ✅
- In-app tab + panel grid + `QueuePanel` (tier border, NEW chip, re-request badge, click-through, size, chips) → Task 7. ✅
- Tests at every layer + visual check → Tasks 1–8. ✅

**Open verification points resolved:** OVP1 (`additions/deletions/changedFiles` on the open-PR query — Task 1 query edit + test). OVP2 (UpsertPR `ON CONFLICT` extended — Task 2 (d)). OVP3 (`isAwaiting` used only in `Run` — Task 5 removes it; grep guard noted).

**Placeholder scan:** none — every code step shows full code.

**Type consistency:** `QueueReviewer{Login,Status,ReRequested}`, `QueueRow{…,Awaiting,Tier,Additions,Deletions,ChangedFiles,LastActivityHours}`, `RankQueue(rows, staleHours)`, `Queue(now)`, `buildReviewers(fp)`, `lastActivity(fp)`, `httpserver.New(st, assets, runDigest, webhook, staleHours)`, the `/api/queue` JSON keys, and the `QueuePanel` prop shape all match across producing/consuming tasks.

**Behavioral notes for the executor:**
- Task 3 deletes `reviewerStatus` (and possibly `splitNonEmpty`) — grep first; `DistinctReviewers` stays.
- Task 6 adds a 5th param to `httpserver.New`; seven existing `server_test.go` call sites + `main.go` must pass it or the build breaks.
- The committed `prs` snapshot from earlier phases lacks the new columns until a live poll repopulates it — expected; the migration backfills the columns (defaults), the poll fills the data.
