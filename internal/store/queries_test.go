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
		Reviewers: []QueueReviewer{
			{Login: "alice", Status: "commented"},
			{Login: "carol", Status: "pending"},
		},
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

func TestQueueComputesAwaitingAndSize(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Now()
	if err := st.UpsertPR(PR{
		Repo: "acme/widgets", Number: 7, Title: "t", Author: "alice", URL: "u",
		ReadyAt: now.Add(-72 * time.Hour), LastActivity: now.Add(-2 * time.Hour),
		Additions: 210, Deletions: 18, ChangedFiles: 4,
		Reviewers: []QueueReviewer{
			{Login: "bob", Status: "pending", ReRequested: true},
			{Login: "carol", Status: "approved"},
		},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := st.Queue(now)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	r := rows[0]
	if r.Additions != 210 || r.Deletions != 18 || r.ChangedFiles != 4 {
		t.Errorf("size = +%d -%d /%d", r.Additions, r.Deletions, r.ChangedFiles)
	}
	if len(r.Reviewers) != 2 || !r.Reviewers[0].ReRequested {
		t.Errorf("reviewers = %+v", r.Reviewers)
	}
	if !r.Awaiting {
		t.Errorf("Awaiting = false, want true (bob pending)")
	}
	if r.LastActivityHours < 1.5 || r.LastActivityHours > 2.5 {
		t.Errorf("LastActivityHours = %v, want ~2", r.LastActivityHours)
	}
}

func TestQueueAwaitingRule(t *testing.T) {
	cases := []struct {
		name      string
		reviewers []QueueReviewer
		want      bool
	}{
		{"none", nil, true},
		{"a pending", []QueueReviewer{{Login: "a", Status: "pending"}}, true},
		{"commented only", []QueueReviewer{{Login: "a", Status: "commented"}}, true},
		{"all approved", []QueueReviewer{{Login: "a", Status: "approved"}}, false},
		{"changes", []QueueReviewer{{Login: "a", Status: "changes"}}, false},
		{"approved+pending", []QueueReviewer{{Login: "a", Status: "approved"}, {Login: "b", Status: "pending"}}, true},
	}
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Now()
	for i, c := range cases {
		st.UpsertPR(PR{Repo: "r", Number: i + 1, Author: "x", ReadyAt: now, Reviewers: c.reviewers})
	}
	rows, _ := st.Queue(now)
	got := map[int]bool{}
	for _, r := range rows {
		got[r.PRNumber] = r.Awaiting
	}
	for i, c := range cases {
		if got[i+1] != c.want {
			t.Errorf("%s: Awaiting = %v, want %v", c.name, got[i+1], c.want)
		}
	}
}

func TestRankQueueTiersAndOrder(t *testing.T) {
	rows := []QueueRow{
		{PRNumber: 1, AgeHours: 100, Awaiting: true},  // urgent (>48)
		{PRNumber: 2, AgeHours: 30, Awaiting: true},   // waiting (24..48)
		{PRNumber: 3, AgeHours: 5, Awaiting: true},    // new (<24)
		{PRNumber: 4, AgeHours: 200, Awaiting: false}, // reviewed (not awaiting)
		{PRNumber: 5, AgeHours: 80, Awaiting: true},   // urgent, older than #1? no — younger
	}
	out := RankQueue(rows, 48)
	tier := map[int]string{}
	var order []int
	for _, r := range out {
		tier[r.PRNumber] = r.Tier
		order = append(order, r.PRNumber)
	}
	if tier[1] != "urgent" || tier[2] != "waiting" || tier[3] != "new" || tier[4] != "reviewed" || tier[5] != "urgent" {
		t.Fatalf("tiers = %v", tier)
	}
	// urgent first, oldest-first within tier: 1(100),5(80), then 2, then 3, then 4
	want := []int{1, 5, 2, 3, 4}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order = %v, want %v", order, want)
			break
		}
	}
}
