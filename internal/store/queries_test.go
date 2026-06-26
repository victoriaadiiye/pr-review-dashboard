package store

import (
	"testing"
	"time"
)

func seed(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Roster: alice (member), carol (member, no reviews -> zero row).
	s.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	s.UpsertPerson(Person{Login: "carol", DisplayName: "Carol", Team: "member", Active: true})
	// Guest reviewer dave appears via events only.
	s.UpsertPerson(Person{Login: "dave", DisplayName: "Dave", Team: "guest", Active: true})
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	mustEvent(t, s, "alice", "h1", 13, now.Add(-1*time.Hour))    // this week
	mustEvent(t, s, "alice", "h2", 3, now.Add(-10*24*time.Hour)) // last month-ish, not this week
	mustEvent(t, s, "dave", "h3", 5, now.Add(-2*time.Hour))      // this week
	return s
}

func mustEvent(t *testing.T, s *Store, who, hash string, pts int, at time.Time) {
	t.Helper()
	if err := s.UpsertReviewEvent(ReviewEvent{
		Repo: "acme/widgets", PRNumber: 1, Reviewer: who, State: "COMMENTED",
		Points: pts, RawHash: hash, SubmittedAt: at,
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
}

func TestLeaderboardWeekIncludesZerosAndGuests(t *testing.T) {
	s := seed(t)
	defer s.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	rows, err := s.Leaderboard("week", now)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	// alice 13, dave 5 (guest), carol 0. Ranked desc.
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].Login != "alice" || rows[0].Points != 13 || rows[0].Rank != 1 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].Login != "dave" || !rows[1].IsGuest {
		t.Errorf("row1 = %+v, want guest dave", rows[1])
	}
	if rows[2].Login != "carol" || rows[2].Points != 0 {
		t.Errorf("row2 = %+v, want carol 0", rows[2])
	}
}

func TestLeaderboardUnionsCommentPoints(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	st.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	// One review worth 5, one comment worth 6 -> Points 11, Reviews 1.
	st.UpsertReviewEvent(ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 5, RawHash: "rh1", SubmittedAt: now})
	if err := st.UpsertCommentEvent(CommentEvent{Repo: "r", PRNumber: 1, Author: "alice", Kind: "issue", BodyLen: 300, HasImage: true, Points: 6, RawHash: "ch1", CreatedAt: now}); err != nil {
		t.Fatalf("UpsertCommentEvent: %v", err)
	}
	// A guest who only left a comment must still appear.
	if err := st.UpsertCommentEvent(CommentEvent{Repo: "r", PRNumber: 2, Author: "dave", Kind: "issue", BodyLen: 10, Points: 1, RawHash: "ch2", CreatedAt: now}); err != nil {
		t.Fatalf("UpsertCommentEvent dave: %v", err)
	}
	st.EnsurePerson(Person{Login: "dave", DisplayName: "dave", Team: "guest", Active: true})

	board, err := st.Leaderboard("all", now)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	byLogin := map[string]LeaderRow{}
	for _, r := range board {
		byLogin[r.Login] = r
	}
	if a := byLogin["alice"]; a.Points != 11 || a.Reviews != 1 {
		t.Errorf("alice = %+v, want Points 11 Reviews 1", a)
	}
	if d, ok := byLogin["dave"]; !ok || d.Points != 1 {
		t.Errorf("dave = %+v (ok=%v), want Points 1", d, ok)
	}
}

func TestUpsertCommentEventDedupes(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Now()
	e := CommentEvent{Repo: "r", PRNumber: 1, Author: "alice", Kind: "issue", BodyLen: 10, Points: 1, RawHash: "same", CreatedAt: now}
	if err := st.UpsertCommentEvent(e); err != nil {
		t.Fatalf("first: %v", err)
	}
	e.Points = 6 // re-score on re-fetch
	if err := st.UpsertCommentEvent(e); err != nil {
		t.Fatalf("second: %v", err)
	}
	st.UpsertPerson(Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", now)
	for _, r := range board {
		if r.Login == "alice" && r.Points != 6 {
			t.Errorf("alice points = %d, want 6 (deduped, re-scored)", r.Points)
		}
	}
}

func TestQueueDerivesReviewerStatus(t *testing.T) {
	s := seed(t)
	defer s.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	s.UpsertPR(PR{
		Repo: "acme/widgets", Number: 1, Title: "feat", Author: "bob", URL: "u",
		IsDraft: false, ReadyAt: now.Add(-5 * time.Hour),
		RequestedReviewers: []string{"alice", "carol"},
	})
	rows, err := s.Queue(now)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.AgeHours < 4.9 || r.AgeHours > 5.1 {
		t.Errorf("age = %v, want ~5h", r.AgeHours)
	}
	got := map[string]string{}
	for _, rv := range r.Reviewers {
		got[rv.Login] = rv.Status
	}
	if got["alice"] != "commented" {
		t.Errorf("alice status = %q, want commented", got["alice"])
	}
	if got["carol"] != "pending" {
		t.Errorf("carol status = %q, want pending", got["carol"])
	}
}
