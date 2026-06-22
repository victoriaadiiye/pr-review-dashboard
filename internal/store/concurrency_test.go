package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentReadWriteNoLockContention exercises the store the way the
// running app does post-Phase-2: the poller writes review events while the
// digest scheduler and HTTP API read the leaderboard/queue concurrently.
// Against a file-backed SQLite database with an unbounded connection pool this
// surfaces SQLITE_BUSY ("database is locked"); the store must serialize access
// so no caller ever sees a lock error.
func TestConcurrentReadWriteNoLockContention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrency.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.UpsertPerson(Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true}); err != nil {
		t.Fatalf("seed person: %v", err)
	}

	const workers = 8
	const iterations = 60
	now := time.Now()

	var wg sync.WaitGroup
	errs := make(chan error, 4*workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				e := ReviewEvent{
					Repo: "acme/widgets", PRNumber: 1, Reviewer: "alice",
					State: "COMMENTED", Points: 1, SubmittedAt: now,
					RawHash: fmt.Sprintf("hash-%d-%d", w, i),
				}
				if err := s.UpsertReviewEvent(e); err != nil {
					errs <- err
				}
			}
		}(w)
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, err := s.Leaderboard("week", now); err != nil {
					errs <- err
				}
				if _, err := s.Queue(now); err != nil {
					errs <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "locked") || strings.Contains(msg, "busy") {
			t.Fatalf("lock contention error under concurrent read/write: %v", err)
		}
	}
}
