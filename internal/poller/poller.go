// Package poller fetches GitHub data and snapshots PRs into the store. In v2
// scoring happens in the merge webhook; the poller is snapshot-only.
package poller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/store"
)

// Source is the subset of the GitHub client the poller needs (test seam).
type Source interface {
	FetchPullRequests(ctx context.Context, owner, repo string) ([]github.FetchedPR, error)
	TeamMembers(ctx context.Context, org, team string) ([]string, error)
}

// Poller syncs one or more repos and the roster into the store. In v2 it only
// snapshots open PRs for the queue and syncs the roster; scoring happens in the
// merge webhook.
type Poller struct {
	src      Source
	st       *store.Store
	excluded map[string]bool // bot/service logins kept out of queue reviewer chips
}

// New constructs a Poller.
func New(src Source, st *store.Store) *Poller {
	return &Poller{src: src, st: st}
}

// SetExcludedLogins hides bot/service accounts from the queue's reviewer chips
// (case-insensitive). Mirrors Store.SetExcludedLogins; call once at startup.
func (p *Poller) SetExcludedLogins(logins []string) {
	m := make(map[string]bool, len(logins))
	for _, l := range logins {
		if l = strings.TrimSpace(l); l != "" {
			m[strings.ToLower(l)] = true
		}
	}
	p.excluded = m
}

// SyncRepo fetches open PRs for repo ("owner/name") and snapshots them for the
// queue. It does not score reviews — that is the merge webhook's job.
func (p *Poller) SyncRepo(ctx context.Context, repo string) error {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return fmt.Errorf("bad repo %q, want owner/name", repo)
	}
	prs, err := p.src.FetchPullRequests(ctx, owner, name)
	if err != nil {
		return err
	}
	open := make([]int, 0, len(prs))
	for _, fp := range prs {
		if err := p.st.UpsertPR(store.PR{
			Repo: repo, Number: fp.Number, Title: fp.Title, Author: fp.Author, URL: fp.URL,
			IsDraft: fp.IsDraft, ReadyAt: fp.ReadyAt, MergedAt: fp.MergedAt, UpdatedAt: fp.UpdatedAt,
			RequestedReviewers: fp.RequestedReviewers,
			RequestedTeams:     fp.RequestedTeams,
			Additions:          fp.Additions,
			Deletions:          fp.Deletions,
			ChangedFiles:       fp.ChangedFiles,
			LastActivity:       lastActivity(fp),
			Reviewers:          buildReviewers(fp, p.excluded),
			CommitsSinceReview: commitsSinceReview(fp),
		}); err != nil {
			return err
		}
		open = append(open, fp.Number)
	}
	// GitHub's open-PR list is authoritative: any stored PR for this repo that is
	// no longer in it has merged or closed, so drop it from the queue. Without
	// this, a PR that merges between polls lingers in the queue forever.
	return p.st.MarkRepoPRsClosedExcept(repo, open, time.Now())
}

// SyncRoster pulls team ("org/slug") members as member, and tags any other reviewer
// already seen in events as a guest.
func (p *Poller) SyncRoster(ctx context.Context, team string) error {
	org, slug, ok := splitRepo(team)
	if !ok {
		return fmt.Errorf("bad team %q, want org/slug", team)
	}
	members, err := p.src.TeamMembers(ctx, org, slug)
	if err != nil {
		return err
	}
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m] = true
		if err := p.st.UpsertPerson(store.Person{Login: m, DisplayName: m, Team: "member", Active: true}); err != nil {
			return err
		}
	}
	guests, err := p.st.DistinctReviewers()
	if err != nil {
		return err
	}
	for _, g := range guests {
		if memberSet[g] {
			continue
		}
		if err := p.st.UpsertPerson(store.Person{Login: g, DisplayName: g, Team: "guest", Active: true}); err != nil {
			return err
		}
	}
	return nil
}

// buildReviewers derives per-reviewer status for an open PR. Participants are
// the union of currently-requested reviewers, anyone who submitted a review, and
// anyone who left an issue comment (bots in excluded and the PR author are
// dropped). Status precedence per person:
//
//   - approved / changes  — their latest formal review verdict
//   - commented           — they engaged (a COMMENTED review or an issue
//     comment) but gave no approve/changes verdict; treated as a light
//     changes-requested rather than "still waiting"
//   - pending             — requested but no engagement at all
//
// re_requested marks a currently-requested reviewer who has a prior review
// (the author asked them to look again).
func buildReviewers(fp github.FetchedPR, excluded map[string]bool) []store.QueueReviewer {
	skip := func(login string) bool {
		return login == "" || login == fp.Author || excluded[strings.ToLower(login)]
	}

	type rev struct {
		state string
		at    time.Time
	}
	latest := map[string]rev{}
	for _, r := range fp.Reviews {
		if skip(r.Author) {
			continue
		}
		if cur, ok := latest[r.Author]; !ok || r.SubmittedAt.After(cur.at) {
			latest[r.Author] = rev{state: r.State, at: r.SubmittedAt}
		}
	}
	commented := map[string]bool{}
	for _, c := range fp.Comments {
		if skip(c.Author) {
			continue
		}
		commented[c.Author] = true
	}
	requested := map[string]bool{}
	for _, l := range fp.RequestedReviewers {
		if skip(l) {
			continue
		}
		requested[l] = true
	}

	// Ordered, de-duplicated participant list: requested first, then everyone
	// else who reviewed or commented.
	seen := map[string]bool{}
	var participants []string
	add := func(l string) {
		if !seen[l] {
			seen[l] = true
			participants = append(participants, l)
		}
	}
	for l := range requested {
		add(l)
	}
	for l := range latest {
		add(l)
	}
	for l := range commented {
		add(l)
	}
	sort.Strings(participants)

	out := make([]store.QueueReviewer, 0, len(participants))
	for _, l := range participants {
		r, reviewed := latest[l]
		out = append(out, store.QueueReviewer{
			Login:       l,
			Status:      statusFor(r.state, reviewed, commented[l]),
			ReRequested: requested[l] && reviewed,
		})
	}
	return out
}

// statusFor resolves a participant's queue status. A formal approve/changes
// verdict wins; otherwise any comment activity (a COMMENTED review or an issue
// comment) reads as "commented"; with no engagement it is "pending".
func statusFor(state string, reviewed, commented bool) string {
	if reviewed {
		switch state {
		case "APPROVED":
			return "approved"
		case "CHANGES_REQUESTED":
			return "changes"
		default: // COMMENTED / unknown review state
			return "commented"
		}
	}
	if commented {
		return "commented"
	}
	return "pending"
}

// commitsSinceReview counts commits pushed after the most recent review by a
// non-author reviewer. It is 0 when the PR has no such review (nothing to be
// "since") — the signal is "the author pushed changes the reviewers haven't seen".
func commitsSinceReview(fp github.FetchedPR) int {
	var lastReview time.Time
	for _, r := range fp.Reviews {
		if r.Author == "" || r.Author == fp.Author {
			continue
		}
		if r.SubmittedAt.After(lastReview) {
			lastReview = r.SubmittedAt
		}
	}
	if lastReview.IsZero() {
		return 0
	}
	n := 0
	for _, c := range fp.CommitDates {
		if c.After(lastReview) {
			n++
		}
	}
	return n
}

// lastActivity is the most recent of the PR's updatedAt and any review time.
func lastActivity(fp github.FetchedPR) time.Time {
	t := fp.UpdatedAt
	for _, r := range fp.Reviews {
		if r.SubmittedAt.After(t) {
			t = r.SubmittedAt
		}
	}
	return t
}

func splitRepo(s string) (string, string, bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
