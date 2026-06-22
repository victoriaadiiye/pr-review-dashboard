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
	mustEvent(t, s, "alice", "h1", 13, now.Add(-1*time.Hour))   // this week
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
