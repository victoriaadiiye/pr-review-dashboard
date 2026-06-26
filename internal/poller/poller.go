// Package poller fetches GitHub data and snapshots PRs into the store. In v2
// scoring happens in the merge webhook; the poller is snapshot-only.
package poller

import (
	"context"
	"fmt"
	"strings"

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
	for _, fp := range prs {
		if err := p.st.UpsertPR(store.PR{
			Repo: repo, Number: fp.Number, Title: fp.Title, Author: fp.Author, URL: fp.URL,
			IsDraft: fp.IsDraft, ReadyAt: fp.ReadyAt, MergedAt: fp.MergedAt, UpdatedAt: fp.UpdatedAt,
			RequestedReviewers: fp.RequestedReviewers,
		}); err != nil {
			return err
		}
	}
	return nil
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

func splitRepo(s string) (string, string, bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
