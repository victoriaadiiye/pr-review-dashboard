package digest

import (
	"context"
	"strings"
	"testing"
	"time"

	"pr-review-dashboard/internal/store"
)

// mockSlack records the most recent PostMessage call.
type mockSlack struct {
	channel string
	text    string
	calls   int
	err     error
}

func (m *mockSlack) PostMessage(_ context.Context, channel, text string) error {
	m.calls++
	m.channel = channel
	m.text = text
	return m.err
}

func TestRunPostsDigest(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "acme/widgets", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 4, RawHash: "h1", SubmittedAt: now})
	// A stale PR: ready 72h ago, no reviews -> awaiting.
	st.UpsertPR(store.PR{Repo: "acme/widgets", Number: 9, Title: "Stale one", Author: "carol", URL: "https://gh/9", IsDraft: false, ReadyAt: now.Add(-72 * time.Hour)})

	m := &mockSlack{}
	d := New(st, m, "C123", 48)
	if err := d.Run(context.Background(), now); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("PostMessage calls = %d, want 1", m.calls)
	}
	if m.channel != "C123" {
		t.Errorf("channel = %q, want C123", m.channel)
	}
	if !strings.Contains(m.text, "Alice") || !strings.Contains(m.text, "acme/widgets#9") {
		t.Errorf("digest text missing expected content:\n%s", m.text)
	}
}

func TestRunAllCaughtUp(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})

	m := &mockSlack{}
	d := New(st, m, "C123", 48)
	if err := d.Run(context.Background(), now); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(m.text, "No PRs waiting") {
		t.Errorf("expected all-caught-up text, got:\n%s", m.text)
	}
}

func TestIsAwaiting(t *testing.T) {
	cases := []struct {
		name string
		row  store.QueueRow
		want bool
	}{
		{"no reviewers", store.QueueRow{}, true},
		{"a pending reviewer", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "pending"}}}, true},
		{"all approved", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "approved"}}}, false},
		{"approved + pending", store.QueueRow{Reviewers: []store.QueueReviewer{{Login: "a", Status: "approved"}, {Login: "b", Status: "pending"}}}, true},
	}
	for _, c := range cases {
		if got := isAwaiting(c.row); got != c.want {
			t.Errorf("%s: isAwaiting = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildMessageHasContent(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{
		{Login: "alice", DisplayName: "Alice", Points: 24, Reviews: 6, Rank: 1},
		{Login: "bob", DisplayName: "Bob", Points: 18, Reviews: 4, Rank: 2},
	}
	stale := []store.QueueRow{
		{Repo: "acme/widgets", PRNumber: 42, Title: "Add foo", Author: "carol", URL: "https://gh/42", AgeHours: 52},
	}
	msg := BuildMessage(leaders, stale, now, 48)

	for _, want := range []string{"Alice", "24", "Bob", "acme/widgets#42", "Add foo", "carol", "52h", "https://gh/42"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
}

func TestBuildMessageAllCaughtUp(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{{Login: "alice", DisplayName: "Alice", Points: 5, Reviews: 1, Rank: 1}}
	msg := BuildMessage(leaders, nil, now, 48)
	if !strings.Contains(msg, "No PRs") {
		t.Errorf("expected all-caught-up line, got:\n%s", msg)
	}
}

func TestBuildMessageNoReviews(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	leaders := []store.LeaderRow{{Login: "alice", DisplayName: "Alice", Points: 0, Reviews: 0, Rank: 1}}
	msg := BuildMessage(leaders, nil, now, 48)
	if !strings.Contains(msg, "No reviews") {
		t.Errorf("expected no-reviews line, got:\n%s", msg)
	}
}
