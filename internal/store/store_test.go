package store

import (
	"testing"
	"time"
)

func TestUpsertReviewEventDedupesByRawHash(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	e := ReviewEvent{
		Repo: "acme/widgets", PRNumber: 1, Reviewer: "alice",
		State: "APPROVED", InlineComments: 0, BodyLen: 5,
		SubmittedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Points: 3, RawHash: "h1",
	}
	if err := s.UpsertReviewEvent(e); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same raw_hash, more points -> updates in place, no new row.
	e.Points = 9
	if err := s.UpsertReviewEvent(e); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count, pts int
	if err := s.db.QueryRow(`SELECT count(*), max(points) FROM review_events`).Scan(&count, &pts); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (dedupe)", count)
	}
	if pts != 9 {
		t.Errorf("points = %d, want 9 (updated)", pts)
	}
}

func TestUpsertPersonAndPR(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true}); err != nil {
		t.Fatalf("person: %v", err)
	}
	if err := s.UpsertPR(PR{Repo: "acme/widgets", Number: 7, Title: "x", Author: "bob", URL: "u", RequestedReviewers: []string{"alice"}}); err != nil {
		t.Fatalf("pr: %v", err)
	}
	var people, prs int
	s.db.QueryRow(`SELECT count(*) FROM people`).Scan(&people)
	s.db.QueryRow(`SELECT count(*) FROM prs`).Scan(&prs)
	if people != 1 || prs != 1 {
		t.Errorf("people=%d prs=%d, want 1/1", people, prs)
	}
}
