# PR Review Leaderboard

A small, self-hosted dashboard that makes code review visible and a little
competitive. It polls GitHub for pull requests and reviews across the repos you
configure, scores each review by **thoroughness** (not just count), and ranks
your team on weekly / monthly / all-time leaderboards. A second panel shows the
live **ready-for-review queue** with per-reviewer status.

Everything ships as a single Go binary with an embedded web UI.

## How it works

```
GitHub GraphQL ──▶ poller (every ~15m) ──▶ scorer ──▶ SQLite
                                                         │
                          HTTP server (:8080) ◀── reads ─┘
                              /                 embedded Vue dashboard
                              /api/leaderboard  ranked people for a window
                              /api/queue        open PRs awaiting review
                              /health /metrics
```

- **Poller** — fetches open PRs, their reviews, review-comment counts, and
  requested reviewers via the GitHub GraphQL API.
- **Scorer** — a pure function turning each review into points (see below).
- **Store** — event-sourced SQLite; any time window is just a `WHERE` clause.
- **HTTP server** — JSON API + the embedded Vue single-page dashboard.
- **Roster** — members of a configured GitHub team are the leaderboard roster
  (everyone shown, even at zero). Reviewers not on the team appear as *guests*.

## Scoring (defaults, all tunable via env)

| Component | Points |
|---|---|
| Base (review submitted) | +2 |
| State: CHANGES_REQUESTED | +3 |
| State: COMMENTED | +2 |
| State: APPROVED (bare LGTM) | +1 |
| Inline comments | +1 each, capped at 10/review |
| Substance bonus (review text > ~280 chars) | +2 |

Self-reviews score 0. A deep review with changes + 6 comments + a writeup ≈ 13;
a bare approve = 3.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `GITHUB_TOKEN` (or `GH_TOKEN`) | — | Token with read access to the repos + team. Dev: `GITHUB_TOKEN=$(gh auth token)` |
| `REPOS` | — | Comma-separated `owner/name` list. Overrides `projects.json`. |
| `ROSTER_TEAM` | — | `org/team` whose members form the leaderboard roster. |
| `DB_PATH` | `/data/leaderboard.db` | SQLite file path. |
| `POLL_INTERVAL` | `15m` | How often to poll GitHub. |
| `HEALTH_PORT` | `8080` | Port for the dashboard + API + health check. |
| `SLACK_BOT_TOKEN` | — | Bot token (`xoxb-…`) for the digest. Enables the digest when set with `DIGEST_CHANNEL_ID`. |
| `DIGEST_CHANNEL_ID` | — | Channel ID the digest posts to. The bot must be invited to it. |
| `STALE_PR_HOURS` | `48` | A PR awaiting review longer than this is flagged in the digest. |
| `PTS_*`, `SUBSTANCE_CHARS` | see table | Scoring overrides. |

Repos can be supplied either via `REPOS` or a `projects.json` file:

```json
{ "projects": { "owner/repo-a": {}, "owner/repo-b": {} } }
```

Copy `projects.example.json` → `projects.json`, or just set `REPOS`.

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
- **Phase 3** — peer "👍 helpful" bonus via Slack reactions.
- Real `/metrics` counters, closed-PR pruning, roster-leaver deactivation, and
  rendering team-requested reviewers as team chips.
