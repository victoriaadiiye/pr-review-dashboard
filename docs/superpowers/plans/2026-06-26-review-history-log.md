# Review History Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a History dashboard page listing one row per `(reviewer, PR)` showing points earned, filterable by reviewer and time window.

**Architecture:** Server-side aggregation mirroring the existing `Leaderboard`/`Queue` pattern. A new `store.ReviewHistory` query groups `review_events` by `(reviewer, repo, pr_number)`, adds matching `comment_events` points, joins `prs`/`people` for display fields. Two new JSON endpoints feed a new thin Vue component and a third dashboard tab.

**Tech Stack:** Go 1.x, `modernc.org/sqlite`, `net/http`; Vue 3 `<script setup>` + TypeScript, Vite, Vitest, `@vue/test-utils`.

## Global Constraints

- Points = `SUM(review_events.points)` + `SUM(comment_events.points)` for the same `(reviewer/author, repo, pr_number)` within the window. Matches the leaderboard.
- Rows are anchored on `review_events`: a PR with only standalone comments (no review) does not appear.
- Window boundaries reuse `store.WindowStart` (Europe/Dublin). Window keys: `week | month | all`. History default window: `all`.
- SQLite single-writer connection pool is already configured in `store.Open`; do not change it.
- Frontend reuses `web/src/styles/theme.css` tokens and existing chip/table styling — no new design system, no new deps.
- Run Go tests with `go test ./internal/...`; frontend tests with `npm --prefix web test`.

---

### Task 1: `store.ReviewHistory` query + type

**Files:**
- Modify: `internal/store/queries.go` (add `HistoryRow` type + `ReviewHistory` method)
- Test: `internal/store/queries_test.go` (add tests)

**Interfaces:**
- Consumes: existing `Store`, `WindowStart`, `UpsertReviewEvent`, `UpsertCommentEvent`, `UpsertPR`, `UpsertPerson`.
- Produces:
  ```go
  type HistoryRow struct {
      Reviewer      string   `json:"reviewer"`
      DisplayName   string   `json:"display_name"`
      Repo          string   `json:"repo"`
      PRNumber      int      `json:"pr_number"`
      Title         string   `json:"title"`
      URL           string   `json:"url"`
      Author        string   `json:"author"`
      Points        int      `json:"points"`
      Reviews       int      `json:"reviews"`
      States        []string `json:"states"`
      LastSubmitted string   `json:"last_submitted"`
  }
  func (s *Store) ReviewHistory(window, reviewer string, now time.Time) ([]HistoryRow, error)
  ```

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/queries_test.go`:

```go
func TestReviewHistoryAggregatesAndAddsComments(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	st.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertPR(PR{Repo: "r", Number: 1, Title: "Feat X", Author: "bob", URL: "http://pr/1"})
	// Two reviews on the same PR by alice: 4 + 3 = 7 review pts, 2 reviews.
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 4, RawHash: "a1", SubmittedAt: now.Add(-2 * time.Hour)})
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "APPROVED", Points: 3, RawHash: "a2", SubmittedAt: now.Add(-1 * time.Hour)})
	// A standalone comment by alice on the same PR: +6.
	st.UpsertCommentEvent(CommentEvent{Repo: "r", PRNumber: 1, Author: "alice", Kind: "issue", BodyLen: 300, Points: 6, RawHash: "c1", CreatedAt: now.Add(-90 * time.Minute)})

	rows, err := st.ReviewHistory("all", "", now)
	if err != nil {
		t.Fatalf("ReviewHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Reviewer != "alice" || r.DisplayName != "Alice" {
		t.Errorf("identity = %+v", r)
	}
	if r.Points != 13 || r.Reviews != 2 {
		t.Errorf("Points=%d Reviews=%d, want 13/2", r.Points, r.Reviews)
	}
	if r.Title != "Feat X" || r.URL != "http://pr/1" || r.Author != "bob" {
		t.Errorf("pr fields = %+v", r)
	}
	wantStates := map[string]bool{"APPROVED": true, "COMMENTED": true}
	if len(r.States) != 2 || !wantStates[r.States[0]] || !wantStates[r.States[1]] {
		t.Errorf("States = %v, want APPROVED+COMMENTED", r.States)
	}
}

func TestReviewHistoryExcludesCommentOnlyPRs(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertPerson(Person{Login: "dave", DisplayName: "Dave", Team: "guest", Active: true})
	// dave only left a comment, no review -> must not appear.
	st.UpsertCommentEvent(CommentEvent{Repo: "r", PRNumber: 9, Author: "dave", Kind: "issue", BodyLen: 10, Points: 1, RawHash: "c9", CreatedAt: now})
	rows, err := st.ReviewHistory("all", "", now)
	if err != nil {
		t.Fatalf("ReviewHistory: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0 (comment-only PR excluded)", len(rows))
	}
}

func TestReviewHistoryFiltersByReviewerAndWindow(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC) // Monday-ish window anchor
	st.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertPerson(Person{Login: "dave", DisplayName: "Dave", Team: "guest", Active: true})
	// alice: one this week, one 10 days ago.
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 5, RawHash: "a1", SubmittedAt: now.Add(-1 * time.Hour)})
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "alice", State: "COMMENTED", Points: 5, RawHash: "a2", SubmittedAt: now.Add(-10 * 24 * time.Hour)})
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 3, Reviewer: "dave", State: "COMMENTED", Points: 5, RawHash: "d1", SubmittedAt: now.Add(-1 * time.Hour)})

	// reviewer filter: only alice's rows.
	rows, _ := st.ReviewHistory("all", "alice", now)
	for _, r := range rows {
		if r.Reviewer != "alice" {
			t.Fatalf("reviewer filter leaked %s", r.Reviewer)
		}
	}
	if len(rows) != 2 {
		t.Errorf("alice all-time rows = %d, want 2", len(rows))
	}
	// window filter: week excludes the 10-day-old PR #2.
	week, _ := st.ReviewHistory("week", "alice", now)
	for _, r := range week {
		if r.PRNumber == 2 {
			t.Errorf("week window leaked old PR #2")
		}
	}
}

func TestReviewHistoryNewestFirst(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	st.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 1, RawHash: "old", SubmittedAt: now.Add(-5 * time.Hour)})
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "alice", State: "COMMENTED", Points: 1, RawHash: "new", SubmittedAt: now.Add(-1 * time.Hour)})
	rows, _ := st.ReviewHistory("all", "", now)
	if len(rows) != 2 || rows[0].PRNumber != 2 {
		t.Errorf("order = %+v, want PR#2 (newest) first", rows)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestReviewHistory -v`
Expected: FAIL — `st.ReviewHistory undefined` / `HistoryRow` undefined (compile error).

- [ ] **Step 3: Add `HistoryRow` type and `ReviewHistory` method**

Add to `internal/store/queries.go` (after the `LeaderRow` block, keep imports `sort`, `strings`, `time` — add `strings` to the import group if absent):

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
	Points        int      `json:"points"`
	Reviews       int      `json:"reviews"`
	States        []string `json:"states"`
	LastSubmitted string   `json:"last_submitted"`
}

// ReviewHistory returns one row per (reviewer, PR), scored within the window,
// newest activity first. reviewer == "" returns all reviewers. Rows are
// anchored on review_events; standalone comment points are added when a review
// row exists for the same (reviewer, repo, pr_number).
func (s *Store) ReviewHistory(window, reviewer string, now time.Time) ([]HistoryRow, error) {
	start := tsOrEmpty(WindowStart(window, now))
	rows, err := s.db.Query(`
SELECT rv.reviewer,
       COALESCE(NULLIF(p.display_name, ''), rv.reviewer) AS display_name,
       rv.repo, rv.pr_number,
       COALESCE(pr.title, '') AS title,
       COALESCE(pr.url, '') AS url,
       COALESCE(pr.author, '') AS author,
       rv.pts + COALESCE(cm.pts, 0) AS points,
       rv.revs AS reviews,
       rv.states AS states,
       rv.last_submitted AS last_submitted
FROM (
  SELECT reviewer, repo, pr_number,
         SUM(points) AS pts, COUNT(*) AS revs,
         GROUP_CONCAT(DISTINCT state) AS states,
         MAX(submitted_at) AS last_submitted
  FROM review_events
  WHERE (submitted_at >= ? OR ? = '')
  GROUP BY reviewer, repo, pr_number
) rv
LEFT JOIN (
  SELECT author, repo, pr_number, SUM(points) AS pts
  FROM comment_events
  WHERE (created_at >= ? OR ? = '')
  GROUP BY author, repo, pr_number
) cm ON cm.author = rv.reviewer AND cm.repo = rv.repo AND cm.pr_number = rv.pr_number
LEFT JOIN prs pr ON pr.repo = rv.repo AND pr.pr_number = rv.pr_number
LEFT JOIN people p ON p.login = rv.reviewer
WHERE (rv.reviewer = ? OR ? = '')`,
		start, start, start, start, reviewer, reviewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HistoryRow
	for rows.Next() {
		var r HistoryRow
		var states sql.NullString
		if err := rows.Scan(&r.Reviewer, &r.DisplayName, &r.Repo, &r.PRNumber,
			&r.Title, &r.URL, &r.Author, &r.Points, &r.Reviews, &states, &r.LastSubmitted); err != nil {
			return nil, err
		}
		if states.String != "" {
			r.States = strings.Split(states.String, ",")
			sort.Strings(r.States)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastSubmitted != out[j].LastSubmitted {
			return out[i].LastSubmitted > out[j].LastSubmitted // newest first (RFC3339 sorts lexically)
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].PRNumber < out[j].PRNumber
	})
	return out, nil
}
```

Note: `queries.go` already imports `sort`, `time`, `database/sql`, `encoding/json`, `log`. Add `"strings"` to that import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestReviewHistory -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Run the full store package to confirm no regressions**

Run: `go test ./internal/store/`
Expected: `ok pr-review-dashboard/internal/store`

- [ ] **Step 6: Commit**

```bash
git add internal/store/queries.go internal/store/queries_test.go
git commit -m "feat(store): ReviewHistory query for per-reviewer PR history"
```

---

### Task 2: `/api/history` and `/api/reviewers` endpoints

**Files:**
- Modify: `internal/httpserver/server.go` (two new routes)
- Test: `internal/httpserver/server_test.go` (add tests)

**Interfaces:**
- Consumes: `store.ReviewHistory(window, reviewer, now)`, `store.DistinctReviewers()`, existing `writeJSON`.
- Produces: HTTP routes `GET /api/history?window=&reviewer=` → `[]store.HistoryRow`; `GET /api/reviewers` → `[]string` sorted.

- [ ] **Step 1: Write the failing tests**

Add to `internal/httpserver/server_test.go`:

```go
func TestHistoryEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertPR(store.PR{Repo: "r", Number: 1, Title: "Feat", Author: "bob", URL: "u"})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "APPROVED", Points: 6, RawHash: "h", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48)
	req := httptest.NewRequest(http.MethodGet, "/api/history?window=all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.HistoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Reviewer != "alice" || rows[0].Points != 6 || rows[0].Title != "Feat" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestHistoryEndpointReviewerFilter(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "APPROVED", Points: 1, RawHash: "a", SubmittedAt: now})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "dave", State: "APPROVED", Points: 1, RawHash: "d", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48)
	req := httptest.NewRequest(http.MethodGet, "/api/history?reviewer=dave", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var rows []store.HistoryRow
	json.Unmarshal(rec.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0].Reviewer != "dave" {
		t.Errorf("rows = %+v, want only dave", rows)
	}
}

func TestReviewersEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "bob", State: "APPROVED", Points: 1, RawHash: "b", SubmittedAt: now})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "alice", State: "APPROVED", Points: 1, RawHash: "a", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48)
	req := httptest.NewRequest(http.MethodGet, "/api/reviewers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var who []string
	if err := json.Unmarshal(rec.Body.Bytes(), &who); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(who) != 2 || who[0] != "alice" || who[1] != "bob" {
		t.Errorf("reviewers = %v, want sorted [alice bob]", who)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpserver/ -run 'TestHistoryEndpoint|TestReviewersEndpoint' -v`
Expected: FAIL — routes return 404 / unmarshal into empty (assertions fail). (`HistoryRow` already exists from Task 1, so this compiles.)

- [ ] **Step 3: Add the routes**

In `internal/httpserver/server.go`, add a `"sort"` import to the import block, and add these handlers inside `New` (after the `/api/queue` block, before `/health`):

```go
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		window := r.URL.Query().Get("window")
		if window == "" {
			window = "all"
		}
		reviewer := r.URL.Query().Get("reviewer")
		rows, err := st.ReviewHistory(window, reviewer, time.Now())
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/api/reviewers", func(w http.ResponseWriter, r *http.Request) {
		who, err := st.DistinctReviewers()
		if err == nil {
			sort.Strings(who)
		}
		writeJSON(w, who, err)
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpserver/ -run 'TestHistoryEndpoint|TestReviewersEndpoint' -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Run the full server package to confirm no regressions**

Run: `go test ./internal/httpserver/`
Expected: `ok pr-review-dashboard/internal/httpserver`

- [ ] **Step 6: Commit**

```bash
git add internal/httpserver/server.go internal/httpserver/server_test.go
git commit -m "feat(api): /api/history and /api/reviewers endpoints"
```

---

### Task 3: `History.vue` component + App tab wiring

**Files:**
- Create: `web/src/components/History.vue`
- Modify: `web/src/App.vue` (third tab, history state, loaders, controls)
- Test: `web/src/__tests__/history.test.ts`

**Interfaces:**
- Consumes: `GET /api/history?window=&reviewer=`, `GET /api/reviewers`.
- Produces: `History.vue` with prop `rows: HistoryRow[]` (shape matches `store.HistoryRow` JSON tags). Presentational only — window/reviewer state lives in `App.vue`.

- [ ] **Step 1: Write the failing component test**

Create `web/src/__tests__/history.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import History from '../components/History.vue'

describe('History', () => {
  it('renders a reviewer/PR row with points and a PR link', () => {
    const rows = [
      {
        reviewer: 'alice', display_name: 'Alice', repo: 'acme/widgets', pr_number: 42,
        title: 'Add caching', url: 'http://gh/pr/42', author: 'bob',
        points: 13, reviews: 2, states: ['APPROVED', 'COMMENTED'],
        last_submitted: '2026-06-15T10:00:00Z',
      },
    ]
    const wrapper = mount(History, { props: { rows } })
    const text = wrapper.text()
    expect(text).toContain('Alice')
    expect(text).toContain('Add caching')
    expect(text).toContain('13')
    expect(text).toContain('acme/widgets#42')
    const link = wrapper.find('a')
    expect(link.attributes('href')).toBe('http://gh/pr/42')
  })

  it('shows an empty state when there are no rows', () => {
    const wrapper = mount(History, { props: { rows: [] } })
    expect(wrapper.text()).toContain('No reviews in this window')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `npm --prefix web test -- --run history`
Expected: FAIL — cannot resolve `../components/History.vue`.

- [ ] **Step 3: Create `History.vue`**

Create `web/src/components/History.vue`:

```vue
<script setup lang="ts">
defineProps<{
  rows: Array<{
    reviewer: string
    display_name: string
    repo: string
    pr_number: number
    title: string
    url: string
    author: string
    points: number
    reviews: number
    states: string[]
    last_submitted: string
  }>
}>()

const stateChip = (s: string) =>
  ({ APPROVED: 'approved', CHANGES_REQUESTED: 'changes', COMMENTED: 'commented' } as Record<string, string>)[s] ?? 'commented'

const rel = (iso: string) => {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  const hours = (Date.now() - then) / 3.6e6
  if (hours < 1) return 'just now'
  if (hours < 24) return `${Math.round(hours)}h ago`
  return `${Math.round(hours / 24)}d ago`
}
</script>

<template>
  <table class="hist">
    <thead>
      <tr>
        <th>Reviewer</th>
        <th>Pull request</th>
        <th>State</th>
        <th class="num">Reviews</th>
        <th class="num">Points</th>
        <th class="num">When</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="r in rows" :key="`${r.reviewer}/${r.repo}/${r.pr_number}`">
        <td class="who"><span class="name">{{ r.display_name }}</span></td>
        <td class="pr">
          <a :href="r.url" target="_blank" rel="noopener">{{ r.title || `${r.repo}#${r.pr_number}` }}</a>
          <span class="ref">{{ r.repo }}#{{ r.pr_number }}</span>
        </td>
        <td>
          <span v-for="s in r.states" :key="s" class="chip" :class="`chip--${stateChip(s)}`">{{ stateChip(s) }}</span>
        </td>
        <td class="num dim">{{ r.reviews }}</td>
        <td class="num points">{{ r.points }}</td>
        <td class="num dim">{{ rel(r.last_submitted) }}</td>
      </tr>
      <tr v-if="rows.length === 0">
        <td colspan="6" class="empty">No reviews in this window.</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.hist {
  width: 100%;
  border-collapse: collapse;
}
th {
  text-align: left;
  font-size: var(--step--2);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-subtle);
  padding: var(--space-2xs) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
}
td {
  padding: var(--space-2xs) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
  vertical-align: middle;
}
tbody tr:last-child td { border-bottom: 0; }
tbody tr { transition: background var(--motion-fast) var(--motion-ease); }
tbody tr:hover { background: var(--bg-row-hover); }

.num {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  text-align: right;
  width: 1%;
  white-space: nowrap;
}
th.num { text-align: right; }

.who .name { color: var(--fg); font-weight: 500; }

.pr { width: 100%; }
.pr a { color: var(--fg); font-weight: 500; text-decoration: none; }
.pr a:hover { color: var(--accent); text-decoration: underline; }
.ref {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
}

.points { color: var(--fg-strong); font-weight: 600; font-size: var(--step-0); }
.dim { color: var(--fg-subtle); }

.chip {
  display: inline-block;
  margin-right: var(--space-2xs);
  font-family: var(--font-mono);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  border: 1px solid var(--border-subtle);
  color: var(--fg-subtle);
  background: var(--bg-sub);
}
.chip--approved { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }

.empty {
  text-align: center;
  color: var(--fg-subtle);
  padding: var(--space-m);
}
</style>
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npm --prefix web test -- --run history`
Expected: PASS (both tests).

- [ ] **Step 5: Wire the third tab into `App.vue`**

In `web/src/App.vue`:

1. Import the component (after the `Queue` import):
   ```ts
   import History from './components/History.vue'
   ```
2. Extend the view union and add state (replace the `view` line and add refs):
   ```ts
   const view = ref<'leaderboard' | 'queue' | 'history'>('leaderboard')
   const history = ref<any[]>([])
   const reviewers = ref<string[]>([])
   const historyReviewer = ref<string>('')
   const historyWindow = ref<'week' | 'month' | 'all'>('all')
   ```
3. Add loaders (after `loadQueue`):
   ```ts
   async function loadHistory() {
     try {
       const res = await fetch(`/api/history?window=${historyWindow.value}&reviewer=${encodeURIComponent(historyReviewer.value)}`)
       if (!res.ok) throw new Error(`history: HTTP ${res.status}`)
       history.value = await res.json()
       error.value = ''
     } catch (e) {
       error.value = e instanceof Error ? e.message : 'Failed to load history'
     }
   }
   async function loadReviewers() {
     try {
       const res = await fetch('/api/reviewers')
       if (!res.ok) throw new Error(`reviewers: HTTP ${res.status}`)
       reviewers.value = await res.json()
     } catch {
       reviewers.value = []
     }
   }
   ```
4. Extend `onMounted` and add a watch:
   ```ts
   onMounted(() => {
     loadBoard()
     loadQueue()
     loadHistory()
     loadReviewers()
   })
   watch(activeWindow, loadBoard)
   watch([historyWindow, historyReviewer], loadHistory)
   ```
5. Add the tab button inside the masthead `.seg` group (after the "Review queue" button):
   ```html
   <button role="tab" :aria-selected="view === 'history'"
     :class="{ seg__opt: true, 'seg__opt--on': view === 'history' }"
     @click="view = 'history'">History</button>
   ```
6. Add the history view block (after the `queue-view` `<section>`):
   ```html
   <section v-else-if="view === 'history'" class="history-view">
     <div class="history-controls">
       <select v-model="historyReviewer" class="reviewer-select" aria-label="Filter by reviewer">
         <option value="">All reviewers</option>
         <option v-for="rv in reviewers" :key="rv" :value="rv">{{ rv }}</option>
       </select>
       <div class="seg" role="tablist" aria-label="History window">
         <button v-for="w in windows" :key="w.key" role="tab"
           :aria-selected="historyWindow === w.key"
           :class="{ seg__opt: true, 'seg__opt--on': historyWindow === w.key }"
           @click="historyWindow = w.key">{{ w.label }}</button>
       </div>
     </div>
     <section class="card">
       <div class="card__head">
         <h2>Review history</h2>
         <span class="card__meta">{{ history.length }} rows</span>
       </div>
       <History :rows="history" />
     </section>
   </section>
   ```
   Note: the existing queue `<section v-else ...>` must become `<section v-else-if="view === 'queue'" ...>` so the history branch is reachable.
7. Add styles for the controls (in the `<style scoped>` block):
   ```css
   .history-controls {
     display: flex;
     justify-content: space-between;
     align-items: center;
     gap: var(--space-s);
     flex-wrap: wrap;
   }
   .reviewer-select {
     font: inherit;
     font-size: var(--step--1);
     color: var(--fg);
     background: var(--bg-card);
     border: 1px solid var(--border);
     border-radius: var(--radius-pill);
     padding: 5px 12px;
   }
   ```

- [ ] **Step 6: Type-check and build the frontend**

Run: `npm --prefix web run build`
Expected: build succeeds (vue-tsc + vite), no type errors. (If `App.vue` uses `any[]` refs the existing pattern already type-checks.)

- [ ] **Step 7: Run the full frontend test suite**

Run: `npm --prefix web test -- --run`
Expected: PASS — `history.test.ts`, `leaderboard.test.ts`, `queue.test.ts`.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/History.vue web/src/App.vue web/src/__tests__/history.test.ts
git commit -m "feat(ui): History tab — per-reviewer PR review log with points"
```

---

### Task 4: Full build + verification

**Files:** none (verification only).

- [ ] **Step 1: Full Go build and test**

Run: `go build ./... && go test ./...`
Expected: build clean; all packages `ok`.

- [ ] **Step 2: Frontend build + test**

Run: `npm --prefix web run build && npm --prefix web test -- --run`
Expected: build succeeds; all test files pass.

- [ ] **Step 3: Confirm the embedded assets path still builds**

The Go server embeds `internal/httpserver/web`. If the build pipeline copies `web/dist` there (check `Taskfile.yaml` / `embed.go`), run the project build task: `task build` (or the documented build command). Expected: success. If no such step exists, skip.

- [ ] **Step 4: Commit any build artifacts only if the repo tracks them**

```bash
git status
# Commit dist/embedded assets ONLY if they are normally tracked; otherwise leave untracked.
```

## Self-Review

**Spec coverage:**
- Row grain (one per reviewer/PR) → Task 1 `GROUP BY reviewer, repo, pr_number`. ✓
- Points = reviews + comments → Task 1 `rv.pts + COALESCE(cm.pts,0)` + test. ✓
- Comment-only exclusion → Task 1 `TestReviewHistoryExcludesCommentOnlyPRs`. ✓
- Reviewer filter → Task 1 SQL + Task 2 endpoint + Task 3 select. ✓
- Time window (week/month/all, default all) → Task 1 `WindowStart`, Task 2 default `"all"`. ✓
- Reverse-chron → Task 1 sort + `TestReviewHistoryNewestFirst`. ✓
- API endpoints → Task 2. ✓
- Third tab + component + controls → Task 3. ✓
- Tests (Go store, Go API, frontend) → Tasks 1–3. ✓

**Placeholder scan:** none — every code/test step has full content.

**Type consistency:** `HistoryRow` fields and JSON tags identical across Task 1 (Go struct), Task 2 (decode target), Task 3 (`History.vue` prop shape + `history.test.ts` fixtures). `ReviewHistory(window, reviewer, now)` signature identical in Tasks 1–2. ✓
