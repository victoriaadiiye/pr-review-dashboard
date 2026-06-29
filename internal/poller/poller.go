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
	src Source
	st  *store.Store
}

// New constructs a Poller.
func New(src Source, st *store.Store) *Poller {
	return &Poller{src: src, st: st}
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
			Additions:          fp.Additions,
			Deletions:          fp.Deletions,
			ChangedFiles:       fp.ChangedFiles,
			LastActivity:       lastActivity(fp),
			Reviewers:          buildReviewers(fp),
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

// mapReviewState maps a GitHub review state to the queue's status vocabulary.
func mapReviewState(s string) string {
	switch s {
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

// buildReviewers derives per-reviewer status for an open PR. A currently
// requested reviewer is pending (they owe a review now) and flagged
// re_requested if they have a prior review; a reviewer who reviewed but is not
// currently requested keeps their latest state. The PR author is excluded.
func buildReviewers(fp github.FetchedPR) []store.QueueReviewer {
	type rev struct {
		state string
		at    time.Time
	}
	latest := map[string]rev{}
	for _, r := range fp.Reviews {
		if r.Author == "" || r.Author == fp.Author {
			continue
		}
		if cur, ok := latest[r.Author]; !ok || r.SubmittedAt.After(cur.at) {
			latest[r.Author] = rev{state: r.State, at: r.SubmittedAt}
		}
	}
	requested := map[string]bool{}
	var reqList []string
	for _, l := range fp.RequestedReviewers {
		if l == "" || l == fp.Author || requested[l] {
			continue
		}
		requested[l] = true
		reqList = append(reqList, l)
	}
	var revList []string
	for l := range latest {
		if !requested[l] {
			revList = append(revList, l)
		}
	}
	sort.Strings(reqList)
	sort.Strings(revList)

	out := make([]store.QueueReviewer, 0, len(reqList)+len(revList))
	for _, l := range reqList {
		_, reviewed := latest[l]
		out = append(out, store.QueueReviewer{Login: l, Status: "pending", ReRequested: reviewed})
	}
	for _, l := range revList {
		out = append(out, store.QueueReviewer{Login: l, Status: mapReviewState(latest[l].state)})
	}
	return out
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
