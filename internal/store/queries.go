package store

import (
	"database/sql"
	"encoding/json"
	"log"
	"sort"
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

// QueueReviewer is a reviewer's status on a queued PR.
type QueueReviewer struct {
	Login       string `json:"login"`
	Status      string `json:"status"` // approved | commented | changes | pending
	ReRequested bool   `json:"re_requested"`
}

// QueueRow is one PR awaiting review.
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
		// Monday 00:00.
		offset := (int(n.Weekday()) + 6) % 7 // Mon=0
		d := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
		return d
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
		out = append(out, r)
	}
	return out, rows.Err()
}
