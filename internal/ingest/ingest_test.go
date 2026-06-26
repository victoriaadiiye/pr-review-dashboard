package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

type fakeFetcher struct {
	detail github.FetchedPRDetail
	calls  int
}

func (f *fakeFetcher) FetchPullRequest(_ context.Context, _, _ string, _ int) (github.FetchedPRDetail, error) {
	f.calls++
	return f.detail, nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestScorePRPersists(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 42, Author: "carol",
		Reviews: []github.FetchedReview{
			{ID: "R1", Author: "alice", State: "CHANGES_REQUESTED", Body: strings.Repeat("x", 300), InlineComments: 0},
		},
		Comments: []github.FetchedComment{
			{ID: "C1", Author: "bob", Body: "great"},
			{ID: "C2", Author: "carol", Body: "self comment ignored"}, // self -> 0, still stored
			{ID: "C3", Author: "", Body: "ghost"},                     // empty author -> skipped
		},
	}}
	ing := New(f, st, scorer.Default())
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 42); err != nil {
		t.Fatalf("ScorePR: %v", err)
	}

	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	pts := map[string]int{}
	for _, r := range board {
		pts[r.Login] = r.Points
	}
	if pts["alice"] != 7 { // CHANGES(2+3) + substance(2)
		t.Errorf("alice = %d, want 7", pts["alice"])
	}
	if pts["bob"] != 1 { // comment base
		t.Errorf("bob = %d, want 1", pts["bob"])
	}
	if _, ok := pts[""]; ok {
		t.Errorf("empty-author row surfaced on board: %v", pts)
	}
}

func TestScorePRIdempotent(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 1, Author: "carol",
		Reviews: []github.FetchedReview{{ID: "R1", Author: "alice", State: "APPROVED", Body: "lgtm"}},
	}}
	ing := New(f, st, scorer.Default())
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := ing.ScorePR(context.Background(), "acme/widgets", "acme", "widgets", 1); err != nil {
		t.Fatalf("second: %v", err)
	}
	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	for _, r := range board {
		if r.Login == "alice" && r.Reviews != 1 {
			t.Errorf("alice Reviews = %d after double ScorePR, want 1 (deduped)", r.Reviews)
		}
	}
}

func timeNowUTC() time.Time { return time.Now().UTC() }
