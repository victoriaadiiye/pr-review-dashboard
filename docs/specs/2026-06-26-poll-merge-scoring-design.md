# Poll-Based Merge Scoring (no webhook) — Design

**Status:** Approved for planning
**Date:** 2026-06-26
**Repo:** `~/projects/pr-review-dashboard`
**Supersedes:** the webhook-as-primary-trigger assumption in `2026-06-24-scoring-v2-merge-driven-design.md`. The scoring *model* (score at merge, thoroughness rewards) is unchanged; only the *trigger* changes.

## Purpose

A GitHub webhook is not available in the target deployment, so merged PRs are never scored and the leaderboard stays at zero. Replace the webhook as the primary scoring trigger with **polling**: the existing poll loop scans recently-merged PRs and scores them. The same mechanism provides a one-time **30-day backfill** on first run, then keeps the board fresh on every cycle.

The webhook is **retained** as an optional, off-by-default alternate trigger (enabled only when `WEBHOOK_SECRET` is set) — no behavior change for deployments that don't set it.

## Scope

- Adds `internal/ingest` (the per-PR scoring pass, extracted from the webhook handler) and `internal/mergescan` (the scan-and-score loop).
- Adds a single-PR-list query to `internal/github`, a key/value `meta` table to `internal/store`, and one config key.
- Slims `internal/webhook` to delegate to `internal/ingest`.
- Does **not** change the scoring model, the leaderboard/queue queries, the roster model, or the Slack digest.

## Behavioral model

- **Scoring still happens at merge.** A review/comment on an unmerged PR scores 0 until that PR merges; a PR closed without merging never scores. This is unchanged from scoring-v2.
- **The trigger is the poll loop, not a webhook delivery.** Every `POLL_INTERVAL` cycle, after the existing roster sync and open-PR snapshot, the scanner ingests merged PRs.
- **Backfill is the first scan.** With no high-water mark for a repo, the scan window is the last `BACKFILL_DAYS` days. There is no separate startup-backfill code path and no "is the DB empty" check — the absence of a high-water mark *is* the backfill condition.
- **Idempotent.** All scoring upserts dedupe on `raw_hash`, so overlapping windows, re-scans, and (if enabled) webhook redelivery never double-count.
- **Disable switch.** `BACKFILL_DAYS=0` disables all merge-scanning (no backfill, no ongoing scan).

## Architecture

```
                         ┌─ poller loop (every POLL_INTERVAL) ──────────────────────┐
GitHub GraphQL ─────────┤  1. SyncRoster        (existing)                          │
                         │  2. SyncRepo per repo (existing: open-PR snapshot → queue)│
                         │  3. mergescan.ScanRepo per repo (NEW):                    │
                         │       since = high-water(repo)  or  now - BACKFILL_DAYS   │
                         │       list merged PRs since → ingest.ScorePR each         │
                         │       → advance high-water on success                     │
                         └───────────────────────────────────────────────────────────┘
                          HTTP server (:8080) ◀── reads ── SQLite
   (POST /webhook/github still works iff WEBHOOK_SECRET set — calls the same ingest.ScorePR)
```

### Components

- **`internal/ingest`** (new) — the single per-PR scoring path, extracted verbatim from today's `(*webhook.handler).score`. Owns the `PRFetcher` interface, the `raw_hash` key helper, guest seeding, and empty-author skipping. Used by both the webhook handler and the merge scanner so scoring can never diverge between triggers.
- **`internal/mergescan`** (new) — the scan-and-score loop per repo: resolve the scan window from the high-water mark, list merged PRs, score each via `ingest`, advance the mark.
- **`internal/github`** — new `FetchMergedPRNumbers` query enumerating merged PR numbers in a window.
- **`internal/store`** — new `meta(key, value)` table + `GetMeta`/`SetMeta`; the per-repo high-water mark lives here.
- **`internal/config`** — new `BackfillDays int` from `BACKFILL_DAYS` (default 30; `0` disables).
- **`internal/webhook`** — slimmed to verify signature, filter events, and delegate to `ingest.ScorePR`.
- **`main.go`** — construct the ingester + scanner; call `scanner.ScanRepo` inside the existing poll loop after `SyncRepo`.

## Interfaces

### `internal/ingest` (extracted from webhook)

```go
type PRFetcher interface {
    FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error)
}

type Ingester struct { /* fetcher PRFetcher; st *store.Store; weights scorer.Weights */ }

func New(fetcher PRFetcher, st *store.Store, w scorer.Weights) *Ingester

// ScorePR fetches one PR's reviews + issue comments, scores each, and upserts
// them (idempotent via raw_hash). Self-authored reviews/comments score 0 but
// are still stored; empty-author (deleted-account) events are skipped entirely.
func (i *Ingester) ScorePR(ctx context.Context, fullName, owner, repo string, number int) error
```

`ScorePR` is the current `score` method moved without behavioral change, including the `hashKey(fullName, number, actor, kind, nodeID, bodyLen)` helper and `seedGuest`.

### `internal/github`

```go
// FetchMergedPRNumbers returns the numbers of PRs merged on/after `since`,
// newest-first. Pages pullRequests(states:MERGED, orderBy UPDATED_AT desc),
// collecting numbers whose mergedAt >= since, and stops paging once a node's
// updatedAt < since (safe: updatedAt >= mergedAt for any PR).
func (c *Client) FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error)
```

GraphQL shape (new query + parse struct, mirroring the existing `prQuery`/`prGQL` pattern):

```graphql
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:MERGED,first:50,after:$cursor,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{ number mergedAt updatedAt }
      pageInfo{ hasNextPage endCursor }
    }
  }
}
```

### `internal/store`

```go
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);

func (s *Store) GetMeta(key string) (string, bool, error) // bool = found
func (s *Store) SetMeta(key, value string) error          // INSERT … ON CONFLICT(key) DO UPDATE
```

Added to the `schema` const. No migration logic needed: `CREATE TABLE IF NOT EXISTS` covers both new and pre-existing databases at `Open`.

### `internal/mergescan`

```go
type Ingester interface {
    ScorePR(ctx context.Context, fullName, owner, repo string, number int) error
}
type Lister interface {
    FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error)
}

type Scanner struct { /* lister Lister; ingester Ingester; st *store.Store; backfillDays int */ }

func New(lister Lister, ingester Ingester, st *store.Store, backfillDays int) *Scanner

// ScanRepo ingests PRs merged in repo ("owner/name") since the high-water mark
// (or now-backfillDays on first run), then advances the mark on full success.
// No-op when backfillDays <= 0.
func (s *Scanner) ScanRepo(ctx context.Context, repo string, now time.Time) error
```

`ScanRepo` algorithm:

1. If `backfillDays <= 0` → return nil (disabled).
2. Split `repo` into `owner`/`name`; bad form → return error.
3. Resolve `since`:
   - `v, found := GetMeta("last_merge_scan:" + repo)`; if found, parse RFC3339 → `since`.
   - else `since = now.AddDate(0, 0, -backfillDays)`.
   - Apply overlap: `since = since.Add(-1 * time.Hour)` (idempotency makes overlap free).
4. `numbers, err := lister.FetchMergedPRNumbers(ctx, owner, name, since)`; on error return it (mark not advanced).
5. For each `number`: `ingester.ScorePR(ctx, repo, owner, name, number)`; on error return it (mark not advanced → whole window retries next cycle).
6. On full success: `SetMeta("last_merge_scan:"+repo, now.UTC().Format(time.RFC3339))`.

### `main.go` wiring

```go
ing := ingest.New(github.NewClient(cfg.GitHubToken), st, cfg.Weights)
scanner := mergescan.New(github.NewClient(cfg.GitHubToken), ing, st, cfg.BackfillDays)
// webhook (if enabled) is constructed with the same ing.
```

Inside the existing poll goroutine, after each `SyncRepo`:

```go
if err := scanner.ScanRepo(ctx, repo, time.Now()); err != nil {
    log.Printf("merge scan %s: %v", repo, err)
}
```

The first cycle (no mark) performs the 30-day backfill; subsequent cycles ingest only new merges. Per-repo marks isolate failures: a repo whose scan errors retries its own window without affecting others.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `BACKFILL_DAYS` | `30` | Initial scan window (days) when a repo has no high-water mark, i.e. the backfill depth. `0` disables all merge-scanning. |

Reuses existing `POLL_INTERVAL` (scan cadence) and `GITHUB_TOKEN`/`REPOS`. `WEBHOOK_SECRET` remains the (optional) webhook enable switch.

## Error handling

- Never ignore errors; return them up the stack. `main` logs per-repo scan errors and continues to the next repo (matching the existing `SyncRepo` pattern).
- A failed PR list or a failed `ScorePR` leaves the high-water mark unmoved, so the window retries next cycle. Idempotency makes the retry safe.
- No `panic`. The scanner runs inside the already-backgrounded poll goroutine, so a slow first 30-day sweep does not block HTTP serving.
- `gofumpt`; exported types/funcs documented.

## Testing

- **github** — `FetchMergedPRNumbers` over `httptest` canned JSON: a PR merged before `since` is excluded; paging stops when `updatedAt < since`; numbers returned newest-first; multi-page pagination followed.
- **store** — `meta` round-trip: `GetMeta` miss returns `found=false`; `SetMeta` then `GetMeta` returns the value; a second `SetMeta` overwrites.
- **ingest** — `ScorePR` with a fake `PRFetcher`: a substantial review and an issue comment are scored and persisted; a self-authored event scores 0 but is stored; an empty-author event is skipped; re-running dedupes (re-scores in place). These assertions move from the current webhook test.
- **mergescan** — `ScanRepo` with a fake `Lister` and fake `Ingester`: no mark → `since ≈ now - BACKFILL_DAYS`; existing mark → `since ≈ mark - 1h`; mark advanced only on full success; mark untouched when a `ScorePR` errors; `BACKFILL_DAYS=0` → zero calls to lister/ingester.
- **webhook** — still verifies signature, event filter, and status mapping; delegates to a fake/real ingester (scoring-detail coverage now lives in `ingest`).
- **config** — `BACKFILL_DAYS` parsed; default 30; `0` preserved.
- Stdlib `testing` + `net/http/httptest` + hand-written fakes only. No new dependencies. Full gate: `go build ./... && go test ./... && go vet ./... && gofumpt -l .` clean.

## Build sequence (phases)

Implemented as a single plan with ordered, independently-testable tasks:

1. **store** — `meta` table + `GetMeta`/`SetMeta` (TDD).
2. **github** — `FetchMergedPRNumbers` query + parse + window/pagination logic (TDD).
3. **ingest** — extract the scoring pass from webhook into `internal/ingest`; repoint the webhook handler to it; move the scoring-detail tests. (Build + webhook tests stay green.)
4. **mergescan** — `Scanner.ScanRepo` with the window/high-water/error logic (TDD with fakes).
5. **config** — `BACKFILL_DAYS`; `.env.example`.
6. **main.go** — wire ingester + scanner into the poll loop; full gate; README note that scoring is poll-driven (webhook optional).

## Open verification points (resolve during implementation, do not guess)

1. Confirm GitHub GraphQL `pullRequests(states:MERGED, orderBy:{field:UPDATED_AT, direction:DESC})` returns `mergedAt` non-null for all nodes and that `updatedAt >= mergedAt` holds (basis for the stop-paging cutoff). If a merged PR can have `updatedAt < mergedAt`, widen the stop condition.
2. Confirm the exact set of names/symbols moved in the `ingest` extraction so the webhook package compiles with no dangling references (`grep` for `hashKey`, `seedGuest`, `PRFetcher` before/after).
3. Confirm `intOr` (added in scoring-v2) is the right helper for `BACKFILL_DAYS` and that `0` is preserved (not coerced to the default) — `intOr` returns the parsed value when set, so `BACKFILL_DAYS=0` yields 0.
