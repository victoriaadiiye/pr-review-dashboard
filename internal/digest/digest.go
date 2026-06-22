// Package digest builds and posts the scheduled Slack review digest.
package digest

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"pr-review-dashboard/internal/store"
)

// SlackAPI is the send-only Slack surface the digest needs.
type SlackAPI interface {
	PostMessage(ctx context.Context, channel, text string) error
}

// Digest builds the weekly digest from the store and posts it to Slack.
type Digest struct {
	store      *store.Store
	slack      SlackAPI
	channel    string
	staleHours float64
}

// New constructs a Digest. channel is the Slack channel ID; staleHours is the
// age threshold for flagging a PR as awaiting review.
func New(st *store.Store, slack SlackAPI, channel string, staleHours float64) *Digest {
	return &Digest{store: st, slack: slack, channel: channel, staleHours: staleHours}
}

// Run builds the digest for the given instant and posts it to Slack.
func (d *Digest) Run(ctx context.Context, now time.Time) error {
	leaders, err := d.store.Leaderboard("week", now)
	if err != nil {
		return fmt.Errorf("leaderboard: %w", err)
	}
	queue, err := d.store.Queue(now)
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	var stale []store.QueueRow
	for _, q := range queue {
		if q.AgeHours > d.staleHours && isAwaiting(q) {
			stale = append(stale, q)
		}
	}
	return d.slack.PostMessage(ctx, d.channel, BuildMessage(leaders, stale, now, d.staleHours))
}

// topN caps how many leaders the digest lists.
const topN = 5

// isAwaiting reports whether a queued PR still needs review: it has no
// reviewers yet, or a requested reviewer has only left a comment or not
// reviewed at all (status pending or commented). A PR with an approval or
// changes-requested review is not awaiting.
func isAwaiting(q store.QueueRow) bool {
	if len(q.Reviewers) == 0 {
		return true
	}
	for _, rv := range q.Reviewers {
		if rv.Status == "pending" || rv.Status == "commented" {
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

// dublin returns the Europe/Dublin location, falling back to UTC if the tzdata
// is unavailable.
func dublin() *time.Location {
	loc, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		return time.UTC
	}
	return loc
}

// nextNineAM returns the next 09:00 in loc strictly after now.
func nextNineAM(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), 9, 0, 0, 0, loc)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// RunScheduler blocks, firing Run once per day at 09:00 Europe/Dublin until ctx
// is cancelled. nowFn supplies the current time (injectable for tests).
func (d *Digest) RunScheduler(ctx context.Context, nowFn func() time.Time) {
	loc := dublin()
	for {
		wait := nextNineAM(nowFn(), loc).Sub(nowFn())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			if err := d.Run(rctx, nowFn()); err != nil {
				log.Printf("digest run: %v", err)
			}
			cancel()
		}
	}
}
