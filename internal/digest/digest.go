// Package digest builds and posts the scheduled Slack review digest.
package digest

import (
	"fmt"
	"strings"
	"time"

	"pr-review-dashboard/internal/store"
)

// topN caps how many leaders the digest lists.
const topN = 5

// isAwaiting reports whether a queued PR still needs a review: it has no
// reviewers yet, or at least one requested reviewer has not reviewed.
func isAwaiting(q store.QueueRow) bool {
	if len(q.Reviewers) == 0 {
		return true
	}
	for _, rv := range q.Reviewers {
		if rv.Status == "pending" {
			return true
		}
	}
	return false
}

// BuildMessage renders the Slack mrkdwn digest: the weekly top-N leaderboard
// followed by stale PRs awaiting review. stale is assumed pre-filtered to PRs
// older than staleHours that are still awaiting review.
func BuildMessage(leaders []store.LeaderRow, stale []store.QueueRow, now time.Time, staleHours float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*PR Review Leaderboard — week of %s*\n\n", now.Format("Mon 02 Jan"))

	b.WriteString("🏆 *Top reviewers this week*\n")
	shown := 0
	for _, l := range leaders {
		if l.Points <= 0 {
			continue
		}
		shown++
		name := l.DisplayName
		if name == "" {
			name = l.Login
		}
		fmt.Fprintf(&b, "%d. %s — %d pts (%d reviews)\n", shown, name, l.Points, l.Reviews)
		if shown >= topN {
			break
		}
	}
	if shown == 0 {
		b.WriteString("_No reviews logged yet this week._\n")
	}

	fmt.Fprintf(&b, "\n⏳ *PRs awaiting review > %dh*\n", int(staleHours))
	if len(stale) == 0 {
		fmt.Fprintf(&b, "✅ No PRs waiting longer than %dh — nice work!\n", int(staleHours))
		return b.String()
	}
	for _, q := range stale {
		name := q.Title
		fmt.Fprintf(&b, "• %s#%d _%s_ by %s — %dh %s\n",
			q.Repo, q.PRNumber, name, q.Author, int(q.AgeHours), q.URL)
	}
	return b.String()
}
