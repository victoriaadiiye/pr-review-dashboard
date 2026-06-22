package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	h := New(st, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
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
	h := New(st, fstest.MapFS{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health status = %d", rec.Code)
	}
}
