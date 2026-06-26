# Review Queue Panels — Design

**Status:** Approved for planning
**Date:** 2026-06-26
**Repo:** `~/projects/pr-review-dashboard`
**Builds on:** the poll-driven queue snapshot and the qompass-nexus facelift already on `main`.

## Purpose

Turn the plain "Ready for review" list into a dedicated **Review queue** view of rich, clickable PR panels. Each panel summarizes a PR (author, reviewer states, size, age/last-activity), is colored by urgency, and links to the PR on GitHub. Panels sort urgent-first so a PR that has waited too long surfaces at the top, and re-requested reviewers are called out.

## Scope

- Adds per-PR metadata to the open-PR snapshot: lines changed, changed files, last activity, and per-reviewer state (incl. re-request flag).
- Adds a server-side urgency ranking and a new frontend **Review queue** tab with PR panels.
- Reuses the existing poll loop (no new GitHub calls beyond extra fields on the open-PR query) and the nexus theme.
- Does **not** change scoring, the leaderboard, the merge scanner, or the Slack digest's behavior (the digest's awaiting-rule is consolidated into the store, output unchanged).

## Decisions (locked during brainstorming)

- **Navigation:** an in-app tab toggle (Leaderboard / Review queue), client-side `ref`, no router/dependency.
- **Identity:** no logged-in user. Re-requested reviewers are shown per-panel for everyone (no personalization).
- **Panel metrics:** author; reviewer states (`approved`/`changes`/`commented`/`pending`) with a re-request badge; lines changed (`+adds −dels`) and changed-file count; age and last-activity.
- **Urgency:** tiered left-border on a single, urgent-first sorted list. Thresholds: `urgent` when awaiting review longer than `STALE_PR_HOURS` (existing config, default 48); `new` when younger than 24h; `waiting` in between; `reviewed` (calm) when reviewers have responded (not awaiting). Colors: red / amber / teal / calm-green.
- **Storage:** Approach A — JSON reviewers blob + scalar columns on the `prs` snapshot (no normalized reviewers table).
- **Click target:** the whole panel is a link to the PR (`target=_blank`).

## Architecture

```
GitHub GraphQL (open PRs, now incl. additions/deletions/changedFiles + reviews + reviewRequests)
        │  poller.SyncRepo (every POLL_INTERVAL)
        ▼
prs snapshot  (+ additions, deletions, changed_files, last_activity, reviewers_json)
        │  store.Queue(now)  → enriched QueueRow[] (parses reviewers_json, computes Awaiting)
        ▼
/api/queue handler  → store.RankQueue(rows, now, staleHours)  → tier + urgent-first sort
        ▼
Vue: Review-queue tab → Queue.vue (panel grid) → QueuePanel.vue (one card per PR)
```

## Data model

### GitHub (`internal/github/client.go`)

Extend the open-PR query node (`prQuery`) with `additions deletions changedFiles`. `FetchedPR` gains:

```go
Additions    int
Deletions    int
ChangedFiles int
```

`FetchedReview` already carries `Author, State, SubmittedAt`; `reviewRequests` is already fetched. No new request is made — only extra fields on the existing query.

### Store (`internal/store`)

`prs` gains five columns, added idempotently in `migrate` via the existing `hasColumn` (`PRAGMA table_info`) guard (snapshot rows backfill on the next poll):

```sql
ALTER TABLE prs ADD COLUMN additions      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prs ADD COLUMN deletions      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prs ADD COLUMN changed_files  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prs ADD COLUMN last_activity  TEXT;
ALTER TABLE prs ADD COLUMN reviewers_json TEXT;
```

The same columns are added to the `CREATE TABLE prs` in the schema const so new DBs have them directly (mirrors the `has_image` precedent).

Types:

```go
// QueueReviewer gains ReRequested.
type QueueReviewer struct {
    Login       string `json:"login"`
    Status      string `json:"status"`        // approved | commented | changes | pending
    ReRequested bool   `json:"re_requested"`
}

// QueueRow gains size, activity, awaiting, tier.
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
    Tier              string          `json:"tier"`   // urgent | waiting | new | reviewed
    Reviewers         []QueueReviewer `json:"reviewers"`
}
```

`PR` (snapshot input) gains `Additions, Deletions, ChangedFiles int`, `LastActivity time.Time`, and `Reviewers []QueueReviewer`; `UpsertPR` persists them (`reviewers_json` via `json.Marshal`).

**`Awaiting` rule (consolidated from `digest.isAwaiting`):** `len(Reviewers) == 0` OR any reviewer status is `pending` or `commented`. Computed in `Queue` and set on each row. `digest.Run` switches from its local `isAwaiting(q)` to `q.Awaiting`; `digest.isAwaiting` and its test are removed (the rule is now tested in `store`). Digest output is unchanged.

**`Queue(now)`** (signature unchanged): selects the new columns, parses `reviewers_json` into `Reviewers`, computes `AgeHours` (from `ready_at`) and `LastActivityHours` (from `last_activity`, falling back to `ready_at` when null), and sets `Awaiting`. It does **not** set `Tier` (that is presentation; see `RankQueue`).

**`RankQueue` (new, pure, no DB):**

```go
// newPRHours is the age below which a PR is "new".
const newPRHours = 24

// RankQueue assigns each row's Tier and returns the rows sorted urgent-first.
func RankQueue(rows []QueueRow, staleHours float64) []QueueRow
```

Tier per row:
- `reviewed` if `!Awaiting` (reviewers have approved or requested changes — author's turn);
- else `new` if `AgeHours < newPRHours`;
- else `urgent` if `AgeHours > staleHours`;
- else `waiting`.

Sort: tier rank `urgent(0) < waiting(1) < new(2) < reviewed(3)`, then `AgeHours` descending within a tier (oldest first). Stable.

### Poller (`internal/poller.SyncRepo`)

For each fetched open PR, build the snapshot:
- `Additions/Deletions/ChangedFiles` from the fetch.
- `LastActivity` = the latest of `UpdatedAt` and every review's `SubmittedAt`.
- `Reviewers`: from `RequestedReviewers` ∪ review authors, excluding the PR author. Per login:
  - `Status` = the latest review's state for that login mapped to `approved`/`changes`/`commented`; `pending` if the login has no review;
  - `ReRequested` = the login is in `RequestedReviewers` **and** has at least one prior review.

A small pure helper (`buildReviewers(fp github.FetchedPR) []store.QueueReviewer`) does this derivation so it is unit-testable without a fetch or DB.

## API / wiring

- `/api/queue` returns `RankQueue(st.Queue(now), staleHours)` — tiered and sorted.
- `staleHours` is threaded into `httpserver.New` (new trailing `staleHours float64` param) from `cfg.StalePRHours`; all `New(...)` call sites (server tests + `main.go`) are updated. `/api/leaderboard`, `/health`, `/metrics`, `/digest/run`, `/webhook/github`, and asset serving are unchanged.

## Frontend

- **`App.vue`** — add a top-level view toggle (`view: 'leaderboard' | 'queue'`) styled with the existing segmented-pill control. The week/month/all window control moves to render only within the Leaderboard view. The masthead/theme are shared.
- **`Queue.vue`** — renders the `/api/queue` rows (already ranked) as a responsive grid of `QueuePanel`s; shows an empty state ("✅ Nothing waiting — the queue is clear.") and surfaces fetch errors.
- **`QueuePanel.vue`** (new) — one card, the whole card is an `<a href=url target=_blank>`:
  - header: mono `repo#num` · title · `↗`; a `NEW` chip when `tier === 'new'`;
  - meta: `by author` · `+adds −dels · N files` · `Nh old` · `last activity Nh`;
  - reviewers: tinted status chips (reuse the facelift chip styles) `✓/±/💬/○` + login; a `re-requested` badge on flagged reviewers;
  - `--tier-*` left border + subtle bg tint: `urgent` → `--err`, `waiting` → `--warn`, `new` → `--accent`, `reviewed` → `--ok`/calm; hover lift + accent ring.

## Error handling

- Never ignore errors. `reviewers_json` that fails to unmarshal yields an empty reviewer list for that row (logged), not a failed request — one malformed snapshot row must not break the whole queue.
- `RankQueue` is total: any unknown/empty tier sorts last.
- No panic. `gofumpt`; exported types/funcs documented.

## Testing

- **github** — open-PR query parses `additions/deletions/changedFiles` (httptest canned JSON).
- **poller** — `buildReviewers`: latest-state-per-author; `pending` for requested-but-unreviewed; `ReRequested` true only when requested **and** previously reviewed; PR author excluded. `LastActivity` = max(updatedAt, review times). LOC persisted.
- **store** — `Queue` parses `reviewers_json` and sets `Awaiting` (table-driven: no reviewers, a pending, all approved, approved+pending, commented-only); migration adds the five columns to a pre-existing `prs` row without data loss; `RankQueue` tier assignment (each tier + boundaries at `staleHours` and `newPRHours`) and urgent-first sort order.
- **digest** — unchanged output; `Run` now reads `q.Awaiting` (the moved rule keeps the commented-only-is-awaiting behavior; covered by store tests).
- **frontend (vitest)** — `QueuePanel` renders the tier border class, `NEW` chip, and re-requested badge for given props; `Queue` renders panels in the provided order and shows the empty state.
- **manual** — Playwright screenshot of the Review-queue tab against the qompass snapshot DB to verify tiers/sort/look.
- Stdlib `testing` + `httptest` + vitest only; no new dependencies. Full gate: `go build/test/vet`, `gofumpt -l`, `npm test`.

## Interfaces (consumed/produced)

- `github.FetchedPR` += `Additions, Deletions, ChangedFiles int`.
- `store.PR` += `Additions, Deletions, ChangedFiles int`, `LastActivity time.Time`, `Reviewers []QueueReviewer`.
- `store.QueueReviewer` += `ReRequested bool`; `store.QueueRow` += size/activity/`Awaiting`/`Tier`.
- New `store.RankQueue(rows []QueueRow, staleHours float64) []QueueRow` + `const newPRHours = 24`.
- New `poller` helper `buildReviewers(github.FetchedPR) []store.QueueReviewer` (+ `lastActivity` helper).
- `httpserver.New` gains a trailing `staleHours float64` param.
- New Vue `QueuePanel.vue`; `App.vue` gains a view toggle; `Queue.vue` becomes a panel grid.

## Open verification points (resolve during implementation, do not guess)

1. Confirm GraphQL `pullRequest` exposes `additions`, `deletions`, `changedFiles` on the open-PR list query (they do on the v4 `PullRequest` type) and that the existing `prGQL` parse struct extends cleanly.
2. Confirm `UpsertPR`'s existing `ON CONFLICT` update list is extended to the new columns so re-polled PRs update size/reviewers (not just insert).
3. Confirm `digest`'s only use of `isAwaiting` is the `Run` stale filter before removing it (grep), so consolidation onto `q.Awaiting` leaves no dangling reference.
