package store

import (
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

// QueueReviewer is a reviewer's status on a queued PR.
type QueueReviewer struct {
	Login  string `json:"login"`
	Status string `json:"status"` // approved | commented | changes | pending
}

// QueueRow is one PR awaiting review.
type QueueRow struct {
	Repo      string          `json:"repo"`
	PRNumber  int             `json:"pr_number"`
	Title     string          `json:"title"`
	Author    string          `json:"author"`
	URL       string          `json:"url"`
	AgeHours  float64         `json:"age_hours"`
	Reviewers []QueueReviewer `json:"reviewers"`
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
       COALESCE(SUM(CASE WHEN e.submitted_at >= ? OR ? = '' THEN e.points ELSE 0 END), 0) AS pts,
       COALESCE(SUM(CASE WHEN e.submitted_at >= ? OR ? = '' THEN 1 ELSE 0 END), 0) AS revs
FROM people p
LEFT JOIN review_events e ON e.reviewer = p.login
WHERE p.active = 1
GROUP BY p.login, p.display_name, p.team
HAVING p.team = 'member' OR pts > 0`,
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

// prSeed holds raw PR data before reviewer status is resolved.
type prSeed struct {
	repo      string
	prNumber  int
	title     string
	author    string
	url       string
	readyAt   string
	reviewers string
}

// Queue returns open, non-draft, unmerged PRs with per-requested-reviewer status,
// newest-ready first.
// Note: reviewer status queries are run after the PR cursor is closed to avoid
// SQLite "overlapping reads on the same connection" stalls.
func (s *Store) Queue(now time.Time) ([]QueueRow, error) {
	rows, err := s.db.Query(`
SELECT repo, pr_number, title, author, url, ready_at, requested_reviewers
FROM prs
WHERE is_draft = 0 AND merged_at = ''
ORDER BY ready_at DESC`)
	if err != nil {
		return nil, err
	}

	var seeds []prSeed
	for rows.Next() {
		var p prSeed
		if err := rows.Scan(&p.repo, &p.prNumber, &p.title, &p.author, &p.url, &p.readyAt, &p.reviewers); err != nil {
			rows.Close()
			return nil, err
		}
		seeds = append(seeds, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []QueueRow
	for _, p := range seeds {
		var q QueueRow
		q.Repo = p.repo
		q.PRNumber = p.prNumber
		q.Title = p.title
		q.Author = p.author
		q.URL = p.url
		if t, err := time.Parse(time.RFC3339, p.readyAt); err == nil {
			q.AgeHours = now.Sub(t).Hours()
		}
		for _, login := range splitNonEmpty(p.reviewers) {
			q.Reviewers = append(q.Reviewers, QueueReviewer{
				Login:  login,
				Status: s.reviewerStatus(q.Repo, q.PRNumber, login),
			})
		}
		out = append(out, q)
	}
	return out, nil
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
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

// reviewerStatus returns the latest review state for a reviewer on a PR, mapped
// to a display status. "pending" if they have not reviewed.
func (s *Store) reviewerStatus(repo string, pr int, login string) string {
	var state string
	err := s.db.QueryRow(`
SELECT state FROM review_events
WHERE repo = ? AND pr_number = ? AND reviewer = ?
ORDER BY submitted_at DESC LIMIT 1`, repo, pr, login).Scan(&state)
	if err != nil {
		return "pending"
	}
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes"
	case "COMMENTED":
		return "commented"
	default:
		return "pending"
	}
}
