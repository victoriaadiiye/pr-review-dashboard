package mergescan

import (
	"context"
	"errors"
	"testing"
	"time"

	"pr-review-dashboard/internal/store"
)

type fakeLister struct {
	since   time.Time
	called  int
	numbers []int
}

func (f *fakeLister) FetchMergedPRNumbers(_ context.Context, _, _ string, since time.Time) ([]int, error) {
	f.called++
	f.since = since
	return f.numbers, nil
}

type fakeIngester struct {
	scored []int
	failOn int // PR number to fail on; 0 = never
}

func (f *fakeIngester) ScorePR(_ context.Context, _, _, _ string, number int) error {
	if number == f.failOn {
		return errors.New("boom")
	}
	f.scored = append(f.scored, number)
	return nil
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

func TestScanRepoBackfillWindowOnFirstRun(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{numbers: []int{7}}
	ig := &fakeIngester{}
	s := New(l, ig, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	want := now.AddDate(0, 0, -30)
	if !l.since.Equal(want) {
		t.Errorf("first-run since = %v, want %v (now-30d)", l.since, want)
	}
	if len(ig.scored) != 1 || ig.scored[0] != 7 {
		t.Errorf("scored = %v, want [7]", ig.scored)
	}
	// Mark advanced to now.
	v, found, _ := st.GetMeta("last_merge_scan:acme/widgets")
	if !found || v != now.UTC().Format(time.RFC3339) {
		t.Errorf("mark = %q found=%v, want %q", v, found, now.UTC().Format(time.RFC3339))
	}
}

func TestScanRepoIncrementalUsesMarkMinusOverlap(t *testing.T) {
	st := newStore(t)
	mark := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)
	st.SetMeta("last_merge_scan:acme/widgets", mark.Format(time.RFC3339))
	l := &fakeLister{}
	s := New(l, &fakeIngester{}, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	want := mark.Add(-time.Hour)
	if !l.since.Equal(want) {
		t.Errorf("incremental since = %v, want %v (mark-1h)", l.since, want)
	}
}

func TestScanRepoDoesNotAdvanceMarkOnError(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{numbers: []int{7, 8}}
	ig := &fakeIngester{failOn: 8}
	s := New(l, ig, st, 30)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err == nil {
		t.Fatal("ScanRepo: want error from failing ScorePR, got nil")
	}
	if _, found, _ := st.GetMeta("last_merge_scan:acme/widgets"); found {
		t.Error("mark advanced despite a ScorePR error; window must retry next cycle")
	}
}

func TestScanRepoDisabled(t *testing.T) {
	st := newStore(t)
	l := &fakeLister{}
	ig := &fakeIngester{}
	s := New(l, ig, st, 0)
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if err := s.ScanRepo(context.Background(), "acme/widgets", now); err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if l.called != 0 || len(ig.scored) != 0 {
		t.Errorf("disabled scan touched lister(%d)/ingester(%d), want 0/0", l.called, len(ig.scored))
	}
}

func TestScanRepoBadRepo(t *testing.T) {
	st := newStore(t)
	s := New(&fakeLister{}, &fakeIngester{}, st, 30)
	if err := s.ScanRepo(context.Background(), "noslash", time.Now()); err == nil {
		t.Error("want error for malformed repo, got nil")
	}
}
