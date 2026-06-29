package poller

import (
	"context"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/store"
)

type fakeSource struct {
	prs     []github.FetchedPR
	members []string
}

func (f *fakeSource) FetchPullRequests(_ context.Context, _, _ string) ([]github.FetchedPR, error) {
	return f.prs, nil
}

func (f *fakeSource) TeamMembers(_ context.Context, _, _ string) ([]string, error) {
	return f.members, nil
}

// SyncRepo snapshots PRs for the queue but must NOT score reviews in v2 —
// scoring moved to the merge webhook.
func TestSyncRepoSnapshotsButDoesNotScore(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	src := &fakeSource{prs: []github.FetchedPR{{
		Number: 1, Title: "feat", Author: "bob", URL: "u", IsDraft: false,
		ReadyAt:            time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		RequestedReviewers: []string{"alice"},
		Reviews: []github.FetchedReview{
			{
				Author: "alice", State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400,
				SubmittedAt: time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC),
			},
		},
	}}}
	p := New(src, st)
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The PR is snapshotted into the queue.
	q, err := st.Queue(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(q) != 1 || q[0].PRNumber != 1 {
		t.Fatalf("queue = %+v, want 1 PR #1", q)
	}

	// No review_events were written, so DistinctReviewers is empty.
	revs, err := st.DistinctReviewers()
	if err != nil {
		t.Fatalf("DistinctReviewers: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("DistinctReviewers = %v, want none (poller must not score)", revs)
	}
}

// A PR that merges between polls drops out of GitHub's open-PR list; the poller
// must reconcile and remove it from the queue, not leave it lingering.
func TestSyncRepoDropsMergedPRsFromQueue(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ready := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	src := &fakeSource{prs: []github.FetchedPR{
		{Number: 1, Title: "one", Author: "bob", URL: "u1", ReadyAt: ready},
		{Number: 2, Title: "two", Author: "carol", URL: "u2", ReadyAt: ready},
	}}
	p := New(src, st)
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	now := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	if q, _ := st.Queue(now); len(q) != 2 {
		t.Fatalf("initial queue = %d, want 2", len(q))
	}

	// PR #2 merges -> next poll only returns #1.
	src.prs = []github.FetchedPR{{Number: 1, Title: "one", Author: "bob", URL: "u1", ReadyAt: ready}}
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	q, _ := st.Queue(now)
	if len(q) != 1 || q[0].PRNumber != 1 {
		t.Fatalf("queue after merge = %+v, want only PR #1", q)
	}
}

func TestSyncRosterMarksGuests(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	// Pre-existing event from a non-member reviewer (as the webhook would write).
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "dave", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})
	src := &fakeSource{members: []string{"alice", "carol"}}
	p := New(src, st)
	if err := p.SyncRoster(context.Background(), "acme/reviewers"); err != nil {
		t.Fatalf("roster: %v", err)
	}
	board, _ := st.Leaderboard("all", time.Now())
	teams := map[string]string{}
	for _, r := range board {
		teams[r.Login] = r.Team
	}
	if teams["alice"] != "member" || teams["carol"] != "member" {
		t.Errorf("members not member: %v", teams)
	}
	if teams["dave"] != "guest" {
		t.Errorf("dave team = %q, want guest", teams["dave"])
	}
}

func TestBuildReviewers(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	fp := github.FetchedPR{
		Author:             "alice",
		RequestedReviewers: []string{"bob", "carol"}, // bob re-requested (has prior review), carol fresh request
		Reviews: []github.FetchedReview{
			{Author: "bob", State: "APPROVED", SubmittedAt: base},
			{Author: "dave", State: "COMMENTED", SubmittedAt: base},
			{Author: "dave", State: "CHANGES_REQUESTED", SubmittedAt: base.Add(time.Hour)}, // latest wins
			{Author: "alice", State: "APPROVED", SubmittedAt: base},                        // self — excluded
		},
	}
	got := buildReviewers(fp)
	by := map[string]store.QueueReviewer{}
	for _, r := range got {
		by[r.Login] = r
	}
	if len(got) != 3 {
		t.Fatalf("got %d reviewers, want 3 (bob, carol, dave): %+v", len(got), got)
	}
	if by["bob"].Status != "pending" || !by["bob"].ReRequested {
		t.Errorf("bob = %+v, want pending + re_requested", by["bob"])
	}
	if by["carol"].Status != "pending" || by["carol"].ReRequested {
		t.Errorf("carol = %+v, want pending, not re_requested", by["carol"])
	}
	if by["dave"].Status != "changes" || by["dave"].ReRequested {
		t.Errorf("dave = %+v, want changes (latest), not re_requested", by["dave"])
	}
	if _, ok := by["alice"]; ok {
		t.Errorf("PR author alice must be excluded")
	}
}

func TestLastActivity(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	fp := github.FetchedPR{
		UpdatedAt: base,
		Reviews:   []github.FetchedReview{{Author: "b", State: "COMMENTED", SubmittedAt: base.Add(3 * time.Hour)}},
	}
	if got := lastActivity(fp); !got.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("lastActivity = %v, want %v (latest review)", got, base.Add(3*time.Hour))
	}
}

func TestCommitsSinceReview(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	fp := github.FetchedPR{
		Author: "alice",
		Reviews: []github.FetchedReview{
			{Author: "bob", State: "APPROVED", SubmittedAt: base.Add(2 * time.Hour)},
		},
		CommitDates: []time.Time{
			base.Add(1 * time.Hour), // before review
			base.Add(3 * time.Hour), // after review
			base.Add(4 * time.Hour), // after review
		},
	}
	if got := commitsSinceReview(fp); got != 2 {
		t.Errorf("commitsSinceReview = %d, want 2", got)
	}

	// Author's own reviews don't count as a baseline; no reviewer review -> 0.
	fp.Reviews = []github.FetchedReview{{Author: "alice", State: "COMMENTED", SubmittedAt: base}}
	if got := commitsSinceReview(fp); got != 0 {
		t.Errorf("commitsSinceReview with only author review = %d, want 0", got)
	}
}
