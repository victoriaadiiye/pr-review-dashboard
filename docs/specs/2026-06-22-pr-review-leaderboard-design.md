# PR Review Leaderboard Dashboard — Design

**Status:** Draft for review
**Date:** 2026-06-22
**Repo:** `~/projects/pr-review-dashboard`

## Purpose

A team-wide dashboard that gamifies code review. It tracks who reviews which PRs across the
team's repos, awards points weighted by review *thoroughness* (not just count), and ranks
people on weekly / monthly / all-time leaderboards. A second panel shows the live
"ready-for-review" queue with per-reviewer status. A scheduled Slack digest pushes the
current standings + stale PRs so review stays top-of-mind.

Goal: make reviewing PRs visible, fun, and competitive — and surface PRs that are waiting.

## Scope

- **Repos tracked:** `acme/widgets`, `acme/gadgets`
- **Roster:** seeded from GitHub team `acme/reviewers` — every member appears on the board, even at 0 points.
- **v1 deliverable:** the web dashboard (leaderboard + queue).
- **Phase 2:** scheduled Slack digest.
- **Phase 3 (optional):** peer "👍 helpful" bonus signal via Slack reactions.

## Architecture

Follows a single-binary Docker deployment pattern: a single Go binary, Docker/Compose deployment, `.env` +
`projects.json` config, Taskfile workflow, launchd plist alternative, healthcheck on `:8080`.
The one extension is a real **web server** (dashboard + JSON API) on `:8080`
instead of only health/metrics endpoints.

```
                ┌──────────────────────── pr-review-dashboard (one Go binary) ────────────────────────┐
  GitHub  ─────▶│  poller (every ~15m, GraphQL)  ──▶  scorer  ──▶  SQLite (/data/leaderboard.db)       │
  GraphQL       │                                                      │                               │
                │  HTTP server (:8080)  ◀── reads ─────────────────────┘                               │
                │     /                 → embedded Vue dashboard                                        │
                │     /api/leaderboard  → ranked people for a window                                   │
                │     /api/queue        → ready-for-review PRs                                          │
                │     /health /metrics                                                                 │
                │  digest scheduler (daily 09:00 Europe/Dublin)  ──▶  Slack chat.postMessage           │
                └──────────────────────────────────────────────────────────────────────────────────────┘
```

Single instance is fine — this is a dashboard, not a stateless-critical service. SQLite is the
store; event-sourced rows make any time window a simple `WHERE`.

### Components (isolated, each with one job)

- **poller** — fetches PRs + reviews + review comments + requested reviewers from GitHub GraphQL for each configured repo on an interval. Upserts `prs`, inserts/updates `review_events`. Idempotent via `raw_hash`.
- **scorer** — pure function `score(review) -> points` from the configured weights. No I/O; unit-tested in isolation.
- **store** — SQLite access layer. Owns schema + queries (`leaderboard(window)`, `queue()`, `roster()`).
- **roster sync** — pulls `acme/reviewers` members from GitHub, upserts `people` with `team='member'`, marks leavers inactive. Reviewers seen in events but not on member are upserted with `team='guest'`.
- **httpserver** — serves embedded Vue assets + JSON API. Read-only over the store.
- **digest** — scheduled job; builds the weekly top-N + stale-PR list, posts via Slack bot token.

## Data model (SQLite)

```sql
people(
  login TEXT PRIMARY KEY,
  display_name TEXT,
  team TEXT,
  active INTEGER NOT NULL DEFAULT 1
);

prs(
  repo TEXT, pr_number INTEGER,
  title TEXT, author TEXT, url TEXT,
  is_draft INTEGER, ready_at TEXT, merged_at TEXT, updated_at TEXT,
  requested_reviewers_json TEXT,
  last_synced TEXT,
  PRIMARY KEY (repo, pr_number)
);

review_events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT, pr_number INTEGER,
  reviewer TEXT,
  state TEXT,                  -- APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED
  inline_comment_count INTEGER,
  body_len INTEGER,
  submitted_at TEXT,
  points INTEGER,
  raw_hash TEXT UNIQUE        -- dedupe re-polled reviews; allow point re-calc on change
);
```

Time windows are derived: `WHERE submitted_at >= <start-of-week|month>` (Europe/Dublin
boundaries). No separate weekly/monthly tables.

## Ingestion

- GitHub **GraphQL** per repo: open PRs + PRs updated since last sync, each with `reviews`
  (state, body, submittedAt, author), `reviewThreads`/review comment counts, and
  `reviewRequests`.
- Interval: **15 min** (config `POLL_INTERVAL`).
- Auth: `GITHUB_TOKEN` / `GH_TOKEN` env var. Dev: `GITHUB_TOKEN=$(gh auth token)`. Deploy:
  read-only PAT in a k8s secret (PRs + members on the two repos). SSO-authorized as needed.
- Idempotency: `raw_hash` = hash of (repo, pr, reviewer, state, submittedAt, comment count).
  Re-poll updates `points` if a review gained comments.

## Scoring (defaults — all tunable via `.env`)

Per review submitted:

| Component | Points |
|---|---|
| Base (review submitted) | +2 |
| State: CHANGES_REQUESTED | +3 |
| State: COMMENTED | +2 |
| State: APPROVED (bare LGTM) | +1 |
| Inline comments | +1 each, **capped at 10/review** |
| Substance bonus (body+comments > ~280 chars) | +2 |

Anti-gaming rules:
- Ignore self-reviews (reviewer == PR author).
- One **base** award per (reviewer, PR) per distinct review state change.
- Inline-comment points capped per review (above).
- Bot-authored PRs are still reviewable and count.

Worked examples: deep review w/ changes + 6 comments + writeup ≈ `2+3+6+2 = 13`; bare approve = `3`.

Config keys: `PTS_BASE`, `PTS_CHANGES`, `PTS_COMMENTED`, `PTS_APPROVED`,
`PTS_PER_INLINE`, `PTS_INLINE_CAP`, `PTS_SUBSTANCE`, `SUBSTANCE_CHARS`.

## HTTP API

- `GET /api/leaderboard?window=week|month|all` → `[{login, display_name, team, is_guest, points, reviews, avg_points_per_review, rank, rank_delta}]`, full member roster incl. zeros, plus guests who have points. `is_guest` = `team != 'member'`.
- `GET /api/queue` → `[{repo, pr_number, title, author, url, age_hours, reviewers:[{login, status}]}]` where status ∈ `approved|commented|changes|pending`.
- `GET /health` → 200 ok. `GET /metrics` → JSON counters (last poll time, events, errors).

## Frontend (Vue SFC, embedded)

Vue + Vite toolchain (like a standard Vite/Vue setup). Built assets embedded into the Go binary via
`embed.FS` and served at `/`.

- **Leaderboard panel** — tabs Weekly / Monthly / All-time. Rows: rank, avatar/name, points,
  # reviews, avg pts/review (thoroughness), rank-change arrow. Everyone on the member roster
  shown, zeros included.
- **Ready-for-review queue panel** — open non-draft PRs, sortable by staleness. Per PR:
  author, age, and reviewer status chips (✅ approved / 💬 commented / 🔴 changes / ⏳ pending).

## Slack digest (Phase 2)

- Scheduled daily **09:00 Europe/Dublin** (config `DIGEST_CRON`).
- Posts via **bot token** `chat.postMessage` (Slack app installed from `slack-manifest.yaml`,
  scope `chat:write`). No Socket Mode (send-only).
- Target channel: `DIGEST_CHANNEL_ID`. Bot must be invited to it.
- Content: weekly top-5 leaderboard + PRs awaiting review older than `STALE_PR_HOURS`.

## Deployment & config

Files (all mirroring single-binary reference service):
- `Dockerfile` — multi-stage `golang:1.25-bookworm` → `debian:bookworm-slim`, installs `git`/`ca-certificates`/`gh`. `git-credential-env` helper for `GITHUB_TOKEN`. `VOLUME ["/data"]`. `EXPOSE 8080`. `HEALTHCHECK` on `/health`.
- `docker-compose.yml` — `restart: unless-stopped`, `env_file: .env`, volume for `/data` (SQLite), port `${HEALTH_PORT:-8080}:8080`.
- `Taskfile.yaml` — `build`, `deploy`, `redeploy`, `kill`, `logs`, `status`, `url`.
- `entrypoint.sh` — writes `~/.netrc` from `GH_TOKEN`/`GITHUB_TOKEN`, execs binary.
- `com.youruser.pr-review-dashboard.plist` — macOS launchd alternative.
- `projects.json` — tracked repos: `acme/widgets`, `acme/gadgets`.
- `.env` / `.env.example` — see keys below.
- `slack-manifest.yaml` — already written (send-only Slack app).

`.env` keys:
```
GITHUB_TOKEN=            # or GH_TOKEN; dev: $(gh auth token)
ROSTER_TEAM=acme/reviewers
SLACK_BOT_TOKEN=xoxb-…
DIGEST_CHANNEL_ID=C…
POLL_INTERVAL=15m
DIGEST_CRON=0 9 * * *    # 09:00 Europe/Dublin
STALE_PR_HOURS=48
HEALTH_PORT=8080
# scoring weights (optional overrides): PTS_BASE, PTS_CHANGES, PTS_COMMENTED,
# PTS_APPROVED, PTS_PER_INLINE, PTS_INLINE_CAP, PTS_SUBSTANCE, SUBSTANCE_CHARS
```

Deploy targets: Docker/Compose on a coder box (primary), or Kubernetes like the rest of your platform;
launchd plist for local always-on. SQLite db persisted on the mounted `/data` volume.

## Testing

- **scorer** — table-driven unit tests over review shapes → expected points (incl. caps, self-review, substance threshold).
- **store** — tests against a temp SQLite file: window queries, roster-with-zeros, queue status derivation.
- **poller** — GraphQL responses mocked (fixture JSON); assert correct upserts + `raw_hash` dedupe + point re-calc on changed review.
- **digest** — `mockSlack` implementing a `SlackAPI` interface (mirror single-binary reference service); no real Slack calls. Assert message content for "has PRs" and "all caught up".
- **httpserver** — `httptest` over the API endpoints with a seeded store.
- Stdlib `testing` only, no external test deps (mirror single-binary reference service).

## Build sequence (phases)

1. **v1 — dashboard**: store + scorer + poller + roster sync + HTTP API + Vue panels + Docker/Taskfile/plist. Ships a working leaderboard + queue. No Slack.
2. **Phase 2 — Slack digest**: digest scheduler + `chat.postMessage`. Uses keys already in `.env`.
3. **Phase 3 (optional) — peer 👍 signal**: extend Slack manifest (`reactions:read`, event subs, Socket Mode + app token), award bonus points when a review message is reacted 👍.

## Assumptions / open items

- Digest cadence = daily 09:00 Dublin (default; change `DIGEST_CRON`).
- Scoring weights = defaults above (change via `.env`).
- Roster = member team. Reviewers not on member still earn points and **are shown on the board flagged as "guests"** (distinct badge; included in rankings).
- GitHub token has read access to both repos + can read member membership.
