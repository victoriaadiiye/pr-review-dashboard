# Scoring v2 + Merge-Driven Ingestion — Design

**Status:** Draft for review
**Date:** 2026-06-24
**Repo:** `~/projects/pr-review-dashboard`
**Supersedes:** the "Scoring" and "Ingestion" sections of `2026-06-22-pr-review-leaderboard-design.md`.

## Purpose

Rework how review work earns points. Two changes:

1. **Reward thoroughness explicitly.** A bare rubber-stamp approval earns little; a review with a written rationale earns more; a review with a substantial writeup *and* a screenshot proving the change was tested earns the most. General PR comments — even ones that are not a formal review — also earn points.
2. **Score on merge, not on a timer.** Points for a PR are computed once, when the PR merges, driven by a GitHub webhook. The interval poller no longer scores; it is kept only to feed the live review queue and the stale-PR digest.

Net effect: the leaderboard measures **review work on PRs that actually landed**, and quality (testing proof, written rationale) outscores volume.

## Scope

- Affects `internal/scorer`, `internal/store`, `internal/poller`, `internal/config`, `internal/httpserver`, and adds `internal/webhook`.
- Does **not** change the dashboard panels, the leaderboard windows (week/month/all), the roster model, or the Phase 2 Slack digest behavior — only their data source where noted.
- Repos tracked, roster, and time-window semantics are unchanged from v1.

## Behavioral shift: scoring happens at merge

In v1 the poller fetched reviews every ~15 minutes and scored them continuously, so an open PR's reviews counted immediately. In v2:

- **Points are computed once, when a PR merges.** A review or comment on an open (un-merged) PR contributes **0 points until that PR merges**. If a PR is closed without merging, its reviews never score.
- The **leaderboard reflects merged work only.** This is intentional: it ties review credit to PRs that shipped.
- The **poller no longer writes `review_events`.** It is retained in a reduced role (see "Poller, reduced role").

This is a deliberate, user-confirmed trade-off. It is called out here so the behavior is not mistaken for a regression.

## Architecture

```
                ┌──────────────────── pr-review-dashboard (one Go binary) ─────────────────────┐
 GitHub ───────▶│  webhook receiver  (POST /webhook/github)                                     │
 pull_request   │     verify X-Hub-Signature-256 ──▶ on closed+merged:                          │
 events         │       fetch PR reviews + comments (GraphQL) ──▶ scorer ──▶ SQLite             │
                │                                                                                │
 GitHub ───────▶│  poller (every ~15m)  ──▶  open-PR snapshot only (prs table) + roster sync    │
 GraphQL        │       (NO scoring)                                                            │
                │                                                                                │
                │  HTTP server (:8080)  ◀── reads ── SQLite (review_events + comment_events)     │
                │     / /api/leaderboard /api/queue /health /metrics                             │
                │  digest scheduler (daily 09:00 Europe/Dublin)  ──▶ Slack chat.postMessage      │
                └────────────────────────────────────────────────────────────────────────────────┘
```

### Components

- **webhook** (new, `internal/webhook`) — verifies the GitHub HMAC signature, parses `pull_request` events, and on `action == "closed"` with `pull_request.merged == true` triggers a scoring pass for that PR: fetch all of the PR's reviews + issue comments + standalone review comments, score each, upsert into `review_events` / `comment_events`. Idempotent via `raw_hash`, so webhook redelivery is safe.
- **scorer** (`internal/scorer`) — pure functions. Gains the message bump, the image bonus, and a separate comment-scoring function. New `Weights` fields.
- **store** (`internal/store`) — `review_events` gains a `has_image` column; new `comment_events` table; `Leaderboard` unions points from both tables.
- **poller** (`internal/poller`) — reduced to open-PR snapshotting for the queue + stale digest, plus roster sync. No longer computes or writes `review_events`.
- **config** (`internal/config`) — new `WEBHOOK_SECRET`; new scoring-weight env keys.
- **httpserver** (`internal/httpserver`) — mounts the webhook route.

## Scoring model

All values are tunable via `.env` (see Config). Defaults below.

### Formal reviews (a review submitted with a state)

| Component | Points | Condition |
|---|---|---|
| Base | +2 | every non-self review |
| Message bump | +1 | body non-empty and `body_len ≤ 280` |
| Substance | +2 | `body_len > 280` |
| State APPROVED | +1 | |
| State COMMENTED | +2 | |
| State CHANGES_REQUESTED | +3 | |
| Inline comments | +1 each | capped at 10 per review |
| Image bonus | +5 | `has_image` **and** `body_len > 280` |

- **Message bump and Substance are mutually exclusive.** Empty body → +0; 1–280 chars → +1; >280 chars → +2 (not +3).
- The image bonus is **gated on substance**: a lone screenshot on a short review earns nothing extra. This matches the intent "thorough writeup *with* proof of testing."

### Standalone comments (not part of a formal review)

Two kinds are scored: **issue comments** (general PR conversation) and **single inline comments** (a code comment left without submitting a review).

| Component | Points | Condition |
|---|---|---|
| Base | +1 | each comment, flat |
| Image bonus | +5 | `has_image` **and** `body_len > 280` |

- No length gate and no cap on the flat +1 (user decision: scoring is opaque to users, so no farm incentive to defend against).
- The image bonus rule is identical to reviews, so a substantial comment that includes a screenshot of testing is rewarded the same as a review would be.

### `has_image` detection

A body "has an image" if it contains any of:

- Markdown image syntax: `![` … `](` … `)`
- A GitHub attachment URL host: `user-images.githubusercontent.com` or `github.com/user-attachments/`

This is a pure string check on the fetched body and is unit-tested.

### Anti-gaming (retained from v1)

- Ignore self-reviews and self-comments (actor == PR author).
- One **base** award per `(reviewer, PR, state-change)`; re-submitting the same review state does not re-award base. Enforced by `raw_hash` uniqueness.
- Inline-comment points capped at 10 per review.

### Sample totals

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

## Data model changes

```sql
-- review_events: add image flag
ALTER TABLE review_events ADD COLUMN has_image INTEGER NOT NULL DEFAULT 0;

-- new table for standalone comments
CREATE TABLE IF NOT EXISTS comment_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo        TEXT,
  pr_number   INTEGER,
  author      TEXT,
  kind        TEXT,               -- 'issue' | 'inline'
  body_len    INTEGER,
  has_image   INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT,
  points      INTEGER,
  raw_hash    TEXT UNIQUE         -- dedupe re-fetched comments / webhook redelivery
);
CREATE INDEX IF NOT EXISTS idx_comment_created ON comment_events(created_at);
```

- `ALTER TABLE … ADD COLUMN` is applied idempotently at `Open` (guard: ignore "duplicate column name" the same way the schema bootstrap tolerates re-creation), keeping existing databases migratable without a separate tool.
- `raw_hash` for a comment = hash of `(repo, pr, author, kind, comment_id, body_len)` so an edited comment that changes length re-scores, and redelivery of the same comment does not double-count.

### Leaderboard query

Today `Leaderboard` LEFT JOINs `review_events` and derives both `Points` and `Reviews` from it. v2:

- **Points** = sum of `review_events.points` **plus** `comment_events.points`, both filtered to the window.
- **Reviews** = count of `review_events` rows only (comments do **not** inflate the review count, so `AvgPoints = Points/Reviews` stays meaningful as "points per formal review").

Implementation: keep the existing people-LEFT-JOIN-review_events aggregation, and add a correlated subquery (or second LEFT JOIN with its own GROUP BY) that sums windowed `comment_events.points` per author, added into the `pts` column. The `HAVING p.team = 'member' OR pts > 0` clause continues to apply to the combined total, so a guest who only left comments still appears.

## Merge-driven ingestion

### Webhook endpoint

- Route: `POST /webhook/github`.
- **Signature verification:** compute `HMAC-SHA256(WEBHOOK_SECRET, raw_body)`, hex-encode, compare in constant time against the `X-Hub-Signature-256: sha256=<hex>` header. Reject with `401` on mismatch or missing secret/header. The handler must read the raw body once for both verification and parsing.
- **Event filter:** only act on `X-GitHub-Event: pull_request` with `action == "closed"` and `pull_request.merged == true`. All other events return `204` (acknowledged, ignored) so GitHub does not retry.
- **Action on merge:** extract `repo` (full name) and `pull_request.number`, then run a scoring pass (below). Respond `200` once persisted; on a fetch/store error respond `500` so GitHub retries.

### Scoring pass for one PR

1. Fetch the PR's complete review history via GitHub GraphQL: each review's `state`, `body`, `author`, `submittedAt`, and inline review-comment count; the PR's issue comments (`comments`) with `author`, `body`, `createdAt`; and standalone review comments not tied to a submitted review.
2. For each review: compute `body_len`, `has_image`, `SelfReview`, score it, upsert into `review_events`.
3. For each comment: compute `body_len`, `has_image`, score it, upsert into `comment_events`.
4. All upserts are idempotent via `raw_hash` — re-running the pass (or a webhook redelivery) is a no-op for unchanged data and a re-score for changed data.

The GraphQL query reuses the poller's existing client and review/comment parsing where possible; the difference is it targets a single known PR number rather than scanning open PRs.

### Poller, reduced role

The poller keeps running on `POLL_INTERVAL` but its only responsibilities become:

- Snapshot **open, non-draft, un-merged** PRs into the `prs` table so the `/api/queue` panel and the stale-PR digest have current data.
- **Roster sync** (members of `acme/reviewers` → `people`), unchanged.

It **stops** inserting/updating `review_events`. The scoring path moves entirely to the webhook. (The poller's existing review-fetch code is removed or left dormant; the `prs` upsert and roster sync paths are retained.)

## Configuration

New `.env` keys (added to `internal/config.Config`):

| Key | Default | Meaning |
|---|---|---|
| `WEBHOOK_SECRET` | (none) | HMAC secret for GitHub webhook verification. If empty, `/webhook/github` returns `503` (disabled), mirroring the digest-disabled pattern. |
| `SCORE_IMAGE_BONUS` | `5` | Image (testing-proof) bonus. |
| `SCORE_MESSAGE_BUMP` | `1` | Bump for a non-empty short body. |
| `SCORE_COMMENT_BASE` | `1` | Points per standalone comment. |

Existing weight keys (base, state, inline cap, substance) remain. The webhook is enabled only when `WEBHOOK_SECRET` is set, the same opt-in shape as the Slack digest.

## Error handling

- Never ignore errors; return them up the stack. The webhook handler logs and maps errors to HTTP status (`401` bad signature, `503` disabled, `500` fetch/store failure, `200/204` success).
- Signature comparison uses `hmac.Equal` (constant time).
- A scoring pass that partially fails (e.g., one comment fails to upsert) returns an error so GitHub retries the whole delivery; idempotency makes the retry safe.
- No `panic`. `gofumpt`; exported types/funcs documented.

## Testing

- **scorer** — table-driven over every shape in "Sample totals", plus: message-bump/substance mutual exclusion, image bonus gated on substance (image + short body → no bonus), self-review → 0, inline cap, comment scoring (flat + image bonus). `has_image` detection: markdown image, attachment URL, plain text → false.
- **store** — `comment_events` upsert + dedupe by `raw_hash`; migration adds `has_image` to a pre-existing DB without data loss; `Leaderboard` unions review + comment points while `Reviews` counts reviews only; guest-with-only-comments appears.
- **webhook** — signature verify (valid, invalid, missing) using `httptest`; merged-event fixture triggers a scoring pass (with a fake fetcher) and persists rows; non-merge / non-PR events return `204` without scoring; disabled (no secret) returns `503`.
- **poller** — assert it no longer writes `review_events` (open-PR snapshot + roster only).
- Stdlib `testing` + `net/http/httptest` only; hand-written fakes for the GitHub fetcher and signature inputs. No external test deps.

## Interfaces (consumed/produced)

- `scorer.Weights` gains `ImageBonus, MessageBump, CommentBase int`.
- `scorer.Review` gains `HasImage bool`.
- New `func scorer.Score(r Review, w Weights) int` (extended) and `func scorer.ScoreComment(c Comment, w Weights) int`; `type scorer.Comment struct { BodyLen int; HasImage bool; SelfComment bool }`.
- New `store.CommentEvent` struct + `(*Store).UpsertCommentEvent(CommentEvent) error`.
- New `internal/webhook`: a handler constructor `New(secret string, fetcher PRFetcher, st *store.Store, w scorer.Weights) http.Handler` and a `PRFetcher` interface abstracting the GitHub fetch (so tests inject a fake). Mounted by `httpserver`.

## Open verification points (resolve during implementation, do not guess)

1. Confirm the poller's GitHub GraphQL client exposes (or can expose) a single-PR fetch; if it only scans open PRs, add a targeted query rather than reworking the scan.
2. Confirm SQLite `ALTER TABLE ADD COLUMN` error text for the "already exists" case in `modernc.org/sqlite` so the idempotent-migration guard matches the right error.
3. Confirm how `httpserver.New` should receive the webhook handler (extra param vs. an optional mount), consistent with how `runDigest` was threaded in Phase 2.
4. Confirm the GraphQL field for standalone (single) review comments vs. review-attached inline comments so the two are not double-counted against the review's capped inline tally.

## Build sequence (phases)

This design is implemented as a single plan with ordered, independently-testable tasks:

1. scorer: weights + message bump + image bonus + `ScoreComment` + `has_image` detection (pure, TDD).
2. store: `has_image` column + migration, `comment_events` table + `UpsertCommentEvent`, union leaderboard.
3. webhook: signature verify + event filter + scoring pass with an injected `PRFetcher` (TDD with fakes).
4. config: `WEBHOOK_SECRET` + score-weight keys.
5. httpserver: mount `/webhook/github`.
6. poller: drop review scoring; keep open-PR snapshot + roster.
7. main.go: wire fetcher + webhook handler; build + full suite + vet + gofumpt.
