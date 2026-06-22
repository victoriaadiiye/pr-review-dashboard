// Package poller fetches GitHub data, scores reviews, and persists to the store.
package poller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

// Source is the subset of the GitHub client the poller needs (test seam).
type Source interface {
	FetchPullRequests(ctx context.Context, owner, repo string) ([]github.FetchedPR, error)
	TeamMembers(ctx context.Context, org, team string) ([]string, error)
}

// Poller syncs one or more repos and the roster into the store.
type Poller struct {
	src     Source
	st      *store.Store
	weights scorer.Weights
}

// New constructs a Poller.
func New(src Source, st *store.Store, w scorer.Weights) *Poller {
	return &Poller{src: src, st: st, weights: w}
}

// RawHash is the dedupe key for a review event.
func RawHash(repo string, pr int, reviewer, state string, at time.Time, inline int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, reviewer, state, at.UTC().Format(time.RFC3339), inline)))
	return fmt.Sprintf("%x", sum[:])
}

// SyncRepo fetches all open PRs for repo ("owner/name"), scores reviews, persists.
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
		for _, rv := range fp.Reviews {
			pts := scorer.Score(scorer.Review{
				State: rv.State, InlineComments: rv.InlineComments, BodyLen: rv.BodyLen,
				SelfReview: rv.Author == fp.Author,
			}, p.weights)
			if err := p.st.UpsertReviewEvent(store.ReviewEvent{
				Repo: repo, PRNumber: fp.Number, Reviewer: rv.Author, State: rv.State,
				InlineComments: rv.InlineComments, BodyLen: rv.BodyLen,
				SubmittedAt: rv.SubmittedAt, Points: pts,
				RawHash: RawHash(repo, fp.Number, rv.Author, rv.State, rv.SubmittedAt, rv.InlineComments),
			}); err != nil {
				return err
			}
			// EnsurePerson seeds the reviewer as a guest only if they are not already in
			// people. It never overwrites an existing row, so a roster member whose team
			// was set to "member" by SyncRoster is never downgraded. Sync order does not
			// matter — guest seeding only fills gaps that the roster has not yet claimed.
			if err := p.st.EnsurePerson(store.Person{
				Login: rv.Author, DisplayName: rv.Author, Team: "guest", Active: true,
			}); err != nil {
				return err
			}
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
