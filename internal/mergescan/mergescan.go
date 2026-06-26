// Package mergescan scans a repository for recently-merged PRs and scores them
// via an Ingester. The scan window starts at a per-repo high-water mark (or
// now-backfillDays on the first run), making the first scan a backfill.
package mergescan

import (
	"context"
	"fmt"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/store"
)

// scanOverlap is re-scanned before the high-water mark each cycle; idempotency
// makes the overlap free and guards against clock skew / boundary misses.
const scanOverlap = time.Hour

// Ingester scores a single PR (implemented by *ingest.Ingester).
type Ingester interface {
	ScorePR(ctx context.Context, fullName, owner, repo string, number int) error
}

// Lister lists merged PR numbers since a time (implemented by *github.Client).
type Lister interface {
	FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error)
}

// Scanner ingests merged PRs for a repo on each call, tracking progress with a
// per-repo high-water mark in the store.
type Scanner struct {
	lister       Lister
	ingester     Ingester
	st           *store.Store
	backfillDays int
}

// New constructs a Scanner. backfillDays <= 0 disables scanning entirely.
func New(lister Lister, ingester Ingester, st *store.Store, backfillDays int) *Scanner {
	return &Scanner{lister: lister, ingester: ingester, st: st, backfillDays: backfillDays}
}

// ScanRepo ingests PRs merged in repo ("owner/name") since the high-water mark
// (or now-backfillDays on the first run), then advances the mark on full
// success. A list or score error leaves the mark unadvanced so the window
// retries next cycle. No-op when backfillDays <= 0.
func (s *Scanner) ScanRepo(ctx context.Context, repo string, now time.Time) error {
	if s.backfillDays <= 0 {
		return nil
	}
	owner, name, ok := github.SplitRepo(repo)
	if !ok {
		return fmt.Errorf("bad repo %q, want owner/name", repo)
	}
	since, err := s.since(repo, now)
	if err != nil {
		return err
	}
	numbers, err := s.lister.FetchMergedPRNumbers(ctx, owner, name, since)
	if err != nil {
		return fmt.Errorf("list merged %s: %w", repo, err)
	}
	for _, n := range numbers {
		if err := s.ingester.ScorePR(ctx, repo, owner, name, n); err != nil {
			return fmt.Errorf("score %s#%d: %w", repo, n, err)
		}
	}
	return s.st.SetMeta(metaKey(repo), now.UTC().Format(time.RFC3339))
}

// since resolves the scan start: the stored mark minus the overlap, or
// now-backfillDays when there is no (or an unparseable) mark.
func (s *Scanner) since(repo string, now time.Time) (time.Time, error) {
	v, found, err := s.st.GetMeta(metaKey(repo))
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return now.AddDate(0, 0, -s.backfillDays), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return now.AddDate(0, 0, -s.backfillDays), nil // corrupt mark -> full window
	}
	return t.Add(-scanOverlap), nil
}

func metaKey(repo string) string { return "last_merge_scan:" + repo }
