package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"pr-review-dashboard/internal/store"
)

func TestLeaderboardEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "COMMENTED", Points: 4, RawHash: "h", SubmittedAt: time.Now()})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/leaderboard?window=all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.LeaderRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Login != "alice" || rows[0].Points != 4 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestHistoryEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertPerson(store.Person{Login: "alice", DisplayName: "Alice", Team: "member", Active: true})
	st.UpsertPR(store.PR{Repo: "r", Number: 1, Title: "Feat", Author: "bob", URL: "u"})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "APPROVED", Points: 6, RawHash: "h", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/history?window=all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.HistoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Reviewer != "alice" || rows[0].Points != 6 || rows[0].Title != "Feat" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestHistoryEndpointReviewerFilter(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "alice", State: "APPROVED", Points: 1, RawHash: "a", SubmittedAt: now})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "dave", State: "APPROVED", Points: 1, RawHash: "d", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/history?reviewer=dave", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var rows []store.HistoryRow
	json.Unmarshal(rec.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0].Reviewer != "dave" {
		t.Errorf("rows = %+v, want only dave", rows)
	}
}

func TestReviewersEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 1, Reviewer: "bob", State: "APPROVED", Points: 1, RawHash: "b", SubmittedAt: now})
	st.UpsertReviewEvent(store.ReviewEvent{Repo: "r", PRNumber: 2, Reviewer: "alice", State: "APPROVED", Points: 1, RawHash: "a", SubmittedAt: now})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/reviewers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var who []string
	if err := json.Unmarshal(rec.Body.Bytes(), &who); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(who) != 2 || who[0] != "alice" || who[1] != "bob" {
		t.Errorf("reviewers = %v, want sorted [alice bob]", who)
	}
}

func TestHealthEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health status = %d", rec.Code)
	}
}

func TestDigestRunTrigger(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()

	var called atomic.Int32
	run := func(_ context.Context) error {
		called.Add(1)
		return nil
	}
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, run, nil, 48, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if called.Load() != 1 {
		t.Errorf("runDigest called %d times, want 1", called.Load())
	}
}

func TestDigestRunDisabled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestDigestRunRejectsGET(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, func(_ context.Context) error { return nil }, nil, 48, "", nil)

	req := httptest.NewRequest(http.MethodGet, "/digest/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestSyncRunsAndReports(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()

	var called atomic.Int32
	run := func(_ context.Context) error {
		called.Add(1)
		return nil
	}
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", run)

	req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if called.Load() != 1 {
		t.Errorf("runSync called %d times, want 1", called.Load())
	}
}

func TestSyncDisabled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestSyncRejectsGET(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", func(_ context.Context) error { return nil })

	req := httptest.NewRequest(http.MethodGet, "/api/sync", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestWebhookRouteMounted(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	hook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("scored"))
	})
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, hook, 48, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "scored" {
		t.Errorf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestWebhookRouteDisabled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestQueueEndpointRanksUrgentFirst(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Now()
	// An urgent PR (awaiting, 100h) and a new PR (awaiting, 5h).
	st.UpsertPR(store.PR{
		Repo: "r", Number: 1, Author: "a", URL: "u1", ReadyAt: now.Add(-100 * time.Hour),
		Reviewers: []store.QueueReviewer{{Login: "x", Status: "pending"}},
	})
	st.UpsertPR(store.PR{
		Repo: "r", Number: 2, Author: "a", URL: "u2", ReadyAt: now.Add(-5 * time.Hour),
		Reviewers: []store.QueueReviewer{{Login: "x", Status: "pending"}},
	})

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil, 48, "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []store.QueueRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 || rows[0].PRNumber != 1 || rows[0].Tier != "urgent" || rows[1].Tier != "new" {
		t.Errorf("rows = %+v, want #1 urgent first then #2 new", rows)
	}
}
