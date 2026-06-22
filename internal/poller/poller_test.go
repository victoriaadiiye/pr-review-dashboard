package poller

import (
	"context"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
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

func TestSyncRepoScoresAndStores(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	src := &fakeSource{prs: []github.FetchedPR{{
		Number: 1, Title: "feat", Author: "bob", URL: "u", IsDraft: false,
		ReadyAt:            time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		RequestedReviewers: []string{"alice"},
		Reviews: []github.FetchedReview{
			{Author: "alice", State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400,
				SubmittedAt: time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC)},
			{Author: "bob", State: "APPROVED", BodyLen: 1, // self-review -> 0 points, still stored
				SubmittedAt: time.Date(2026, 6, 10, 10, 35, 0, 0, time.UTC)},
		},
	}}}
	p := New(src, st, scorer.Default())
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	board, _ := st.Leaderboard("all", time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	// alice should have 13 points; bob self-review 0 (and bob not on roster/guest with 0 -> excluded).
	var alice *store.LeaderRow
	for i := range board {
		if board[i].Login == "alice" {
			alice = &board[i]
		}
	}
	if alice == nil || alice.Points != 13 {
		t.Fatalf("alice = %+v, want 13 points", alice)
	}
}

func TestSyncRepoDoesNotDowngradeRosterMember(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	// Pre-seed alice as a member member (as SyncRoster would do).
	if err := st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}
	src := &fakeSource{prs: []github.FetchedPR{{
		Number: 1, Title: "feat", Author: "bob", URL: "u", IsDraft: false,
		ReadyAt: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		Reviews: []github.FetchedReview{
			{Author: "alice", State: "APPROVED", BodyLen: 50,
				SubmittedAt: time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC)},
		},
	}}}
	p := New(src, st, scorer.Default())
	if err := p.SyncRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}
	board, err := st.Leaderboard("all", time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	var alice *store.LeaderRow
	for i := range board {
		if board[i].Login == "alice" {
			alice = &board[i]
		}
	}
	if alice == nil {
		t.Fatal("alice not found in leaderboard")
	}
	if alice.Team != "member" {
		t.Errorf("alice.Team = %q after SyncRepo, want %q", alice.Team, "member")
	}
	if alice.IsGuest {
		t.Errorf("alice.IsGuest = true after SyncRepo, want false")
	}
}

func TestSyncRosterMarksGuests(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	// Pre-existing event from a non-member reviewer.
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "dave", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})
	src := &fakeSource{members: []string{"alice", "carol"}}
	p := New(src, st, scorer.Default())
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
