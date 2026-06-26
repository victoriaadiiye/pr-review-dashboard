# Review History Log — Design

**Date:** 2026-06-26
**Status:** Approved
**Branch:** `worktree-review-history-log`

## Goal

A new dashboard page giving a history of PR reviews: for each PR a person
reviewed, show how many points that reviewer earned. One row per
`(reviewer, PR)` pair, newest activity first, filterable by reviewer and by
time window.

## Decisions (from brainstorming)

- **Row grain:** one row per `(reviewer, PR)`. Review events for that pair are
  aggregated into a single row.
- **Points source:** reviews **plus** standalone comments, matching the
  leaderboard. Points = `SUM(review_events.points)` for the pair
  `+ SUM(comment_events.points)` for the same `(author, repo, pr_number)`.
- **Controls:** reverse-chronological list, a reviewer filter, and a
  week/month/all time-window toggle reusing `store.WindowStart`.
- **Anchor:** rows are anchored on `review_events`. A PR where a person only
  left standalone comments (no submitted review) does **not** appear. The page
  is "PRs reviewed", so a review event is required to list a pair. Comment
  points still augment the total when a review row exists.

## Architecture

Server-side aggregation, mirroring the existing `Leaderboard`/`Queue` pattern:
the store does the math, the API returns ready-to-render rows, the Vue
component is thin. No new tables — all data already exists in `review_events`,
`comment_events`, `prs`, and `people`.

### 1. Data layer — `internal/store/queries.go`

New type:

```go
// HistoryRow is one reviewer's review work on one PR, scored.
type HistoryRow struct {
    Reviewer      string   `json:"reviewer"`
    DisplayName   string   `json:"display_name"`
    Repo          string   `json:"repo"`
    PRNumber      int      `json:"pr_number"`
    Title         string   `json:"title"`
    URL           string   `json:"url"`
    Author        string   `json:"author"`
    Points        int      `json:"points"`         // review pts + comment pts
    Reviews       int      `json:"reviews"`        // count of review_events
    States        []string `json:"states"`         // distinct states, sorted
    LastSubmitted string   `json:"last_submitted"`  // RFC3339, newest event
}
```

New method:

```go
// ReviewHistory returns one row per (reviewer, PR), scored within the window,
// newest activity first. reviewer == "" returns all reviewers.
func (s *Store) ReviewHistory(window, reviewer string, now time.Time) ([]HistoryRow, error)
```

Query shape:

- Base: `review_events` grouped by `(reviewer, repo, pr_number)`, filtered
  `submitted_at >= windowStart OR windowStart == ''` (same idiom as
  `Leaderboard`). Aggregates: `SUM(points)`, `COUNT(*)`, `MAX(submitted_at)`,
  and a `GROUP_CONCAT(DISTINCT state)` for the state chips.
- LEFT JOIN a `comment_events` sub-aggregate keyed `(author, repo, pr_number)`
  summing comment points within the same window (`created_at >= windowStart`).
- LEFT JOIN `prs` on `(repo, pr_number)` for `title`, `url`, `author`.
- LEFT JOIN `people` on `reviewer = login` for `display_name` (fall back to the
  login when absent).
- Optional `reviewer = ?` filter when non-empty.
- `Points = review_pts + COALESCE(comment_pts, 0)`.
- `States`: split the `GROUP_CONCAT` on `,`, sort for stable output.
- Order: `LastSubmitted DESC`. Apply final sort in Go (mirrors `Leaderboard`)
  for deterministic tie-breaking by `(repo, pr_number)`.

`DisplayName` falls back to `Reviewer` if empty. Window boundaries reuse
`WindowStart` (Europe/Dublin), identical to the leaderboard.

### 2. API — `internal/httpserver/server.go`

Two new routes, using the existing `writeJSON` helper:

```
GET /api/history?window=week|month|all&reviewer=<login>
    -> []HistoryRow      window default "all"; reviewer optional
GET /api/reviewers
    -> ["alice","bob",...]   sorted distinct reviewers
```

- `/api/history`: read `window` (default `"all"`) and `reviewer` query params,
  call `st.ReviewHistory(window, reviewer, time.Now())`, `writeJSON`.
- `/api/reviewers`: call `st.DistinctReviewers()`, sort, `writeJSON`. Feeds the
  filter dropdown.
- No auth or pagination — consistent with existing endpoints; dataset is
  team-scale. Errors propagate through `writeJSON` as HTTP 500.

History default window is `"all"` (a history page wants the full record by
default), distinct from the leaderboard's `"week"`.

### 3. Frontend — `web/src/`

**`App.vue`:**

- Extend `view` union to `'leaderboard' | 'queue' | 'history'`; add a third tab
  button "History" in the masthead `.seg` tablist.
- New refs: `history` (rows), `reviewers` (filter options),
  `historyReviewer` (selected login, `''` = all), `historyWindow`
  (`'week' | 'month' | 'all'`, default `'all'`).
- `loadHistory()` fetches `/api/history?window=<historyWindow>&reviewer=<historyReviewer>`.
- `loadReviewers()` fetches `/api/reviewers` once `onMounted`.
- `watch([historyWindow, historyReviewer], loadHistory)`.
- Render `<History :rows="history" ... />` when `view === 'history'`.

**`History.vue`** (new component): prop `rows: HistoryRow[]`, plus controls.

- Controls row (mirrors leaderboard control row): reviewer `<select>`
  (All + each reviewer) bound to `historyReviewer`, and a week/month/all `.seg`
  toggle bound to `historyWindow`. (Window/reviewer state and the `<select>`
  options may live in `App.vue` and pass down via props/`v-model` to keep
  `History.vue` presentational — final wiring decided during implementation,
  but state ownership stays in `App.vue` like `activeWindow`.)
- Table columns: **Reviewer** (display name) · **PR** (title, links to `url`,
  shows `repo#num`) · **State** (chip per state) · **Reviews** (count) ·
  **Points** · **When** (relative time from `last_submitted`).
- Reuse `theme.css` tokens and the card/chip/table styling already used by
  `Leaderboard.vue` and `Queue.vue`. No new design system.
- Empty state: "No reviews in this window."

### 4. Testing

- **Go (`internal/store/queries_test.go`):** table-driven tests for
  `ReviewHistory` using an in-memory store (`:memory:`, existing test pattern):
  - groups multiple review events on one PR into a single row with summed
    points and correct review count;
  - adds standalone comment points to the matching `(reviewer, PR)` row;
  - excludes comment-only PRs (no review event) — confirms the anchor decision;
  - respects the `reviewer` filter;
  - respects the window boundary (event before/after `WindowStart`);
  - orders newest `last_submitted` first.
- **Go (`internal/httpserver/server_test.go`):** `/api/history` returns JSON
  rows and honors `window`/`reviewer` query params; `/api/reviewers` returns a
  sorted list. Follow the existing handler-test pattern.
- **Frontend (`web/src/__tests__/`):** a `history.test.ts` mirroring
  `leaderboard.test.ts` — renders rows, shows points, links PRs, shows the
  empty state. Follow the existing Vitest setup.

## Out of scope (YAGNI)

- Pagination / infinite scroll.
- Comment-only PR rows (no review event).
- CSV export, per-PR drill-down, charts.
- Auth / per-user views beyond the reviewer filter.

## Files touched

- `internal/store/queries.go` — `HistoryRow`, `ReviewHistory`.
- `internal/store/queries_test.go` — `ReviewHistory` tests.
- `internal/httpserver/server.go` — `/api/history`, `/api/reviewers`.
- `internal/httpserver/server_test.go` — endpoint tests.
- `web/src/App.vue` — third tab, history state + loaders.
- `web/src/components/History.vue` — new component.
- `web/src/__tests__/history.test.ts` — component test.
