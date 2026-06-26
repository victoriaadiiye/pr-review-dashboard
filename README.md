# PR Review Leaderboard

A small, self-hosted dashboard that makes code review visible and a little
competitive. It polls GitHub for pull requests and reviews across the repos you
configure, scores each review by **thoroughness** (not just count), and ranks
your team on weekly / monthly / all-time leaderboards. A second panel shows the
live **ready-for-review queue** with per-reviewer status.

Everything ships as a single Go binary with an embedded web UI.

## How it works

```
                         ┌─ poller (every ~15m) ─ queue snapshot + roster sync ─┐
GitHub GraphQL ─────────┤                                                         │
                         │                  GitHub webhook delivery               │
                         └─ /webhook/github (on merge) ─ scorer ─ SQLite ─────────┤
                                                                      │
                          HTTP server (:8080) ◀── reads ──────────────┘
                              /                 embedded Vue dashboard
                              /api/leaderboard  ranked people for a window
                              /api/queue        open PRs awaiting review
                              /health /metrics
```

- **Poller** — fetches open PRs and requested reviewers via the GitHub GraphQL
  API; feeds the ready-for-review queue and syncs the roster. Does not score.
- **Webhook** — triggered by GitHub when a PR is merged; verifies HMAC-SHA256
  signature and scores the merged PR's reviews and comments.
- **Scorer** — a pure function turning each review/comment into points (see below).
- **Store** — event-sourced SQLite; any time window is just a `WHERE` clause.
  Leaderboard reflects merged work only (no points until merge).
- **HTTP server** — JSON API + the embedded Vue single-page dashboard.
- **Roster** — members of a configured GitHub team are the leaderboard roster
  (everyone shown, even at zero). Reviewers not on the team appear as *guests*.

## Scoring (defaults, all tunable via env)

**Reviews** (scored when PR merges):

| Component | Points |
|---|---|
| Base (review submitted) | +2 |
| State: CHANGES_REQUESTED | +3 |
| State: COMMENTED | +2 |
| State: APPROVED (bare LGTM) | +1 |
| Inline comments | +1 each, capped at 10/review |
| Message bump (review text 1–280 chars) | +1 |
| Substance bonus (review text > 280 chars) | +2 |
| Image bonus (testing proof in review, gated on >280 chars) | +5 |

**Issue Comments** (scored when PR merges):

| Component | Points |
|---|---|
| Base (comment submitted) | +1 |
| Image bonus (image in comment, gated on substantial length) | +5 |

Self-reviews and self-comments score 0. A thorough review with CHANGES_REQUESTED +
6 inline comments + a writeup (>280 chars) + testing image ≈ 15; a bare approve
= 3. Inline-comment kinds (review vs. discussion) are not separately tracked.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `GITHUB_TOKEN` (or `GH_TOKEN`) | — | Token with read access to the repos + team. Dev: `GITHUB_TOKEN=$(gh auth token)` |
| `REPOS` | — | Comma-separated `owner/name` list. Overrides `projects.json`. |
| `ROSTER_TEAM` | — | `org/team` whose members form the leaderboard roster. |
| `DB_PATH` | `/data/leaderboard.db` | SQLite file path. |
| `POLL_INTERVAL` | `15m` | How often to poll GitHub. |
| `HEALTH_PORT` | `8080` | Port for the dashboard + API + health check. |
| `WEBHOOK_SECRET` | — | HMAC secret for GitHub webhook verification. Webhook returns 503 if unset. |
| `SLACK_BOT_TOKEN` | — | Bot token (`xoxb-…`) for the digest. Enables the digest when set with `DIGEST_CHANNEL_ID`. |
| `DIGEST_CHANNEL_ID` | — | Channel ID the digest posts to. The bot must be invited to it. |
| `STALE_PR_HOURS` | `48` | A PR awaiting review longer than this is flagged in the digest. |
| `SCORE_MESSAGE_BUMP` | `1` | Points for a review with 1–280 characters. |
| `SCORE_COMMENT_BASE` | `1` | Points for an issue comment. |
| `SCORE_IMAGE_BONUS` | `5` | Points for a testing-proof image (gated on substantial review/comment body). |
| `SUBSTANCE_CHARS` | `280` | Character threshold for substance bonus (+2) and message bump gates. |

Repos can be supplied either via `REPOS` or a `projects.json` file:

```json
{ "projects": { "owner/repo-a": {}, "owner/repo-b": {} } }
```

Copy `projects.example.json` → `projects.json`, or just set `REPOS`.

## GitHub Webhook Setup

To enable merge-driven scoring, configure a GitHub webhook:

1. Go to your GitHub org or repo settings → Webhooks → Add webhook.
2. Set:
   - **Payload URL**: `https://your-leaderboard-domain/webhook/github`
   - **Content type**: `application/json`
   - **Secret**: (copy the value of your `WEBHOOK_SECRET` env var)
   - **Events**: Select "Pull requests" only.
3. Leave other options at defaults.

The app verifies each delivery's `X-Hub-Signature-256` header. Unsigned or invalid
deliveries are rejected with HTTP 401. The webhook is disabled (returns 503) when
`WEBHOOK_SECRET` is unset.

## Quick start (local)

```sh
GITHUB_TOKEN=$(gh auth token) \
REPOS=owner/repo-a,owner/repo-b \
ROSTER_TEAM=your-org/your-team \
DB_PATH=./leaderboard.db \
go run .
# open http://localhost:8080
```

## Docker

```sh
cp .env.example .env   # fill in GITHUB_TOKEN, REPOS, ROSTER_TEAM
docker compose up -d --build
```

The SQLite database persists on the mounted `/data` volume.

## Slack digest

A scheduled digest posts the weekly top-5 leaderboard plus PRs awaiting review
longer than `STALE_PR_HOURS` to a Slack channel.

- **Enable it:** set both `SLACK_BOT_TOKEN` and `DIGEST_CHANNEL_ID`. With either
  unset the digest is off and the app logs `digest disabled` at startup.
- **Slack app:** install from `slack-manifest.yaml` (send-only, scope
  `chat:write`). Invite the bot to the target channel.
- **Schedule:** daily at 09:00 Europe/Dublin (in-process ticker).
- **Manual trigger:** `POST /digest/run` sends the digest immediately —
  useful for testing without waiting for 09:00. Returns `503` when the digest
  is not configured.

```sh
curl -X POST http://localhost:8080/digest/run
```

## Development

```sh
go test ./...          # backend
cd web && npm install && npm test   # frontend component tests
cd web && npm run build             # rebuild embedded assets into internal/httpserver/web
```

Built with Go (stdlib + `modernc.org/sqlite`, a pure-Go driver, so the binary
builds with `CGO_ENABLED=0`) and Vue 3 + Vite. Tests use the Go standard library
only.

## Roadmap

- **Phase 2** ✅ — scheduled Slack digest (top of the board + stale PRs). See [Slack digest](#slack-digest).
- Real `/metrics` counters, closed-PR pruning, roster-leaver deactivation, and
  rendering team-requested reviewers as team chips.
