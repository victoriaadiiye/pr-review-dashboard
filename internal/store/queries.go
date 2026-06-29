package store

import (
	"database/sql"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"
)

// LeaderRow is one ranked person on the leaderboard.
type LeaderRow struct {
	Login       string  `json:"login"`
	DisplayName string  `json:"display_name"`
	Team        string  `json:"team"`
	IsGuest     bool    `json:"is_guest"`
	Points      int     `json:"points"`
	Reviews     int     `json:"reviews"`
	AvgPoints   float64 `json:"avg_points_per_review"`
	Rank        int     `json:"rank"`
}

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
		if s.isExcluded(r.Reviewer) {
			continue // bots and service accounts never appear in review history
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

// QueueReviewer is a reviewer's status on a queued PR.
type QueueReviewer struct {
	Login       string `json:"login"`
	Status      string `json:"status"` // approved | commented | changes | pending
	ReRequested bool   `json:"re_requested"`
}

// QueueRow is one PR awaiting review.
type QueueRow struct {
	Repo               string          `json:"repo"`
	PRNumber           int             `json:"pr_number"`
	Title              string          `json:"title"`
	Author             string          `json:"author"`
	URL                string          `json:"url"`
	AgeHours           float64         `json:"age_hours"`
	LastActivityHours  float64         `json:"last_activity_hours"`
	Additions          int             `json:"additions"`
	Deletions          int             `json:"deletions"`
	ChangedFiles       int             `json:"changed_files"`
	CommitsSinceReview int             `json:"commits_since_review"`
	RequestedTeams     []string        `json:"requested_teams"`
	Awaiting           bool            `json:"awaiting"`
	Tier               string          `json:"tier"`
	Reviewers          []QueueReviewer `json:"reviewers"`
	// Relation is this PR's relationship to the requesting user ("me" query
	// param): author | todo_action | todo_done | other. Empty when no user is
	// selected. Set by AssignQueueRelations, not stored.
	Relation string `json:"relation"`
}

// WindowStart returns the inclusive lower bound for a leaderboard window.
// "all" returns the zero time. Boundaries are computed in Europe/Dublin.
func WindowStart(window string, now time.Time) time.Time {
	loc, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	switch window {
	case "week":
		// Rolling last 7 days (not calendar week): the leaderboard must never
		// reset to zero at the start of a calendar week. Anchored to 00:00 so
		// the boundary is stable through the day.
		today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
		return today.AddDate(0, 0, -6) // today + 6 prior days = 7-day window
	case "month":
		return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	default: // all
		return time.Time{}
	}
}

// Leaderboard returns all active people (member roster + guests with activity),
// ranked by points within the window, descending. Zero-point roster members included.
func (s *Store) Leaderboard(window string, now time.Time) ([]LeaderRow, error) {
	start := WindowStart(window, now)
	rows, err := s.db.Query(`
SELECT p.login, p.display_name, p.team,
       COALESCE(rv.pts, 0) + COALESCE(cm.pts, 0) AS pts,
       COALESCE(rv.revs, 0) AS revs
FROM people p
LEFT JOIN (
  SELECT reviewer, SUM(points) AS pts, COUNT(*) AS revs
  FROM review_events WHERE submitted_at >= ? OR ? = '' GROUP BY reviewer
) rv ON rv.reviewer = p.login
LEFT JOIN (
  SELECT author, SUM(points) AS pts
  FROM comment_events WHERE created_at >= ? OR ? = '' GROUP BY author
) cm ON cm.author = p.login
WHERE p.active = 1
  AND (p.team = 'member' OR (COALESCE(rv.pts, 0) + COALESCE(cm.pts, 0)) > 0)`,
		tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start), tsOrEmpty(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LeaderRow
	for rows.Next() {
		var r LeaderRow
		if err := rows.Scan(&r.Login, &r.DisplayName, &r.Team, &r.Points, &r.Reviews); err != nil {
			return nil, err
		}
		if s.isExcluded(r.Login) {
			continue // bots and service accounts never appear on the leaderboard
		}
		r.IsGuest = r.Team != "member"
		if r.Reviews > 0 {
			r.AvgPoints = float64(r.Points) / float64(r.Reviews)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		return out[i].Login < out[j].Login
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, rows.Err()
}

// prSeed holds raw PR data before enrichment.
type prSeed struct {
	repo, title, author, url string
	prNumber                 int
	readyAt, lastActivity    string
	additions, deletions     int
	changedFiles             int
	commitsSinceReview       int
	reviewersJSON            string
	requestedTeams           string
}

// Queue returns open, non-draft, unmerged PRs enriched with size, activity, and
// reviewer state, newest-ready first. Tier/sort is applied by RankQueue.
func (s *Store) Queue(now time.Time) ([]QueueRow, error) {
	rows, err := s.db.Query(`
SELECT repo, pr_number, title, author, url, ready_at, last_activity,
       additions, deletions, changed_files, commits_since_review, reviewers_json, requested_teams
FROM prs
WHERE is_draft = 0 AND merged_at = ''
ORDER BY ready_at DESC`)
	if err != nil {
		return nil, err
	}
	var seeds []prSeed
	for rows.Next() {
		var p prSeed
		var lastActivity, reviewersJSON, requestedTeams sql.NullString
		if err := rows.Scan(&p.repo, &p.prNumber, &p.title, &p.author, &p.url, &p.readyAt,
			&lastActivity, &p.additions, &p.deletions, &p.changedFiles, &p.commitsSinceReview, &reviewersJSON, &requestedTeams); err != nil {
			rows.Close()
			return nil, err
		}
		p.lastActivity = lastActivity.String
		p.reviewersJSON = reviewersJSON.String
		p.requestedTeams = requestedTeams.String
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
			CommitsSinceReview: p.commitsSinceReview,
		}
		if p.requestedTeams != "" {
			q.RequestedTeams = strings.Split(p.requestedTeams, ",")
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
// reviewer is still pending, has only commented (a light changes-requested), or
// was re-requested after a prior review (the author wants another look).
func awaiting(reviewers []QueueReviewer) bool {
	if len(reviewers) == 0 {
		return true
	}
	for _, rv := range reviewers {
		if rv.Status == "pending" || rv.Status == "commented" || rv.ReRequested {
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
		case out[i].AgeHours > staleHours:
			// Check urgent before new: if staleHours < newPRHours, a young-but-stale PR
			// must be marked urgent, not new, to avoid hiding urgent PRs.
			out[i].Tier = "urgent"
		case out[i].AgeHours < newPRHours:
			out[i].Tier = "new"
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

// Queue relation values, set on QueueRow.Relation by AssignQueueRelations.
const (
	RelAuthor     = "author"      // me created this PR — never in my todo
	RelTodoAction = "todo_action" // requested of me / my team / re-requested, not yet done
	RelTodoDone   = "todo_done"   // I reviewed it; sinks to the bottom of my todo
	RelOther      = "other"       // everything else
)

// AssignQueueRelations tags each row's Relation relative to the user me (a
// GitHub login). meIsMember reports whether me belongs to the roster team and
// rosterSlug is that team's slug, together resolving "a team you're on is
// requested". When me is empty every Relation is cleared (no personalization).
//
// The split the UI draws: Todo = {todo_action, todo_done}; All open =
// {author, other}.
func AssignQueueRelations(rows []QueueRow, me string, meIsMember bool, rosterSlug string) {
	for i := range rows {
		rows[i].Relation = relationFor(rows[i], me, meIsMember, rosterSlug)
	}
}

func relationFor(row QueueRow, me string, meIsMember bool, rosterSlug string) string {
	if me == "" {
		return ""
	}
	if strings.EqualFold(row.Author, me) {
		return RelAuthor
	}
	var (
		mine         QueueReviewer
		hasEntry     bool
		reviewedByMe bool
	)
	for _, rv := range row.Reviewers {
		if strings.EqualFold(rv.Login, me) {
			mine, hasEntry = rv, true
			switch rv.Status {
			case "approved", "changes", "commented":
				reviewedByMe = true
			}
			break
		}
	}
	teamRequested := meIsMember && rosterSlug != "" && containsFold(row.RequestedTeams, rosterSlug)

	actionNeeded := (hasEntry && (mine.Status == "pending" || mine.ReRequested)) ||
		(teamRequested && !reviewedByMe)
	switch {
	case actionNeeded:
		return RelTodoAction
	case reviewedByMe:
		return RelTodoDone
	default:
		return RelOther
	}
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// DistinctReviewers returns every login that has submitted a review event.
func (s *Store) DistinctReviewers() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT reviewer FROM review_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		if s.isExcluded(r) {
			continue // keep bots out of the reviewer filter
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PersonInfo is a selectable account for the dashboard's "who am I" picker.
type PersonInfo struct {
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Team        string `json:"team"`
}

// People returns active people for the account picker — roster members first,
// then guests — excluding bots/service accounts. Each is a selectable account.
func (s *Store) People() ([]PersonInfo, error) {
	rows, err := s.db.Query(`
SELECT login, COALESCE(NULLIF(display_name, ''), login), team
FROM people WHERE active = 1
ORDER BY (team = 'member') DESC, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonInfo
	for rows.Next() {
		var p PersonInfo
		if err := rows.Scan(&p.Login, &p.DisplayName, &p.Team); err != nil {
			return nil, err
		}
		if s.isExcluded(p.Login) {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonTeam returns a login's team ("member"/"guest") and whether it is known.
func (s *Store) PersonTeam(login string) (string, bool) {
	var team string
	err := s.db.QueryRow(`SELECT team FROM people WHERE login = ?`, login).Scan(&team)
	if err != nil {
		return "", false
	}
	return team, true
}
