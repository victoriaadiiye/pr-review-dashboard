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

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil)
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

func TestHealthEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	h := New(st, fstest.MapFS{}, nil, nil)
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
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, run, nil)

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
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil)

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
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, func(_ context.Context) error { return nil }, nil)

	req := httptest.NewRequest(http.MethodGet, "/digest/run", nil)
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
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, hook)

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
	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
