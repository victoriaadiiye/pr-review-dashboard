package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

type fakeFetcher struct {
	detail github.FetchedPRDetail
	calls  int
}

func (f *fakeFetcher) FetchPullRequest(_ context.Context, _, _ string, _ int) (github.FetchedPRDetail, error) {
	f.calls++
	return f.detail, nil
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, h http.Handler, event, sig string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(string(body)))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const mergedBody = `{"action":"closed","pull_request":{"number":42,"merged":true},"repository":{"full_name":"acme/widgets"}}`

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMergedEventScoresAndPersists(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 42, Author: "carol",
		Reviews: []github.FetchedReview{
			{ID: "R1", Author: "alice", State: "CHANGES_REQUESTED", Body: strings.Repeat("x", 300), InlineComments: 0},
		},
		Comments: []github.FetchedComment{
			{ID: "C1", Author: "bob", Body: "great"},
			{ID: "C2", Author: "carol", Body: "self comment ignored"}, // self -> 0, still stored
		},
	}}
	h := New("sekret", f, st, scorer.Default())

	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1", f.calls)
	}

	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	pts := map[string]int{}
	for _, r := range board {
		pts[r.Login] = r.Points
	}
	if pts["alice"] != 7 { // CHANGES(2+3) + substance(2)
		t.Errorf("alice points = %d, want 7", pts["alice"])
	}
	if pts["bob"] != 1 { // comment base
		t.Errorf("bob points = %d, want 1", pts["bob"])
	}
}

func TestBadSignatureRejected(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "sha256=deadbeef", body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMissingSignatureRejected(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDisabledWhenNoSecret(t *testing.T) {
	st := newStore(t)
	h := New("", &fakeFetcher{}, st, scorer.Default())
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", "", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNonMergeEventIgnored(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{}
	h := New("sekret", f, st, scorer.Default())
	body := []byte(`{"action":"opened","pull_request":{"number":1,"merged":false},"repository":{"full_name":"acme/widgets"}}`)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if f.calls != 0 {
		t.Errorf("fetcher called %d times on non-merge, want 0", f.calls)
	}
}

func TestNonPREventIgnored(t *testing.T) {
	st := newStore(t)
	h := New("sekret", &fakeFetcher{}, st, scorer.Default())
	body := []byte(`{}`)
	rec := post(t, h, "push", sign("sekret", body), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestEmptyAuthorEventsNotPersisted(t *testing.T) {
	st := newStore(t)
	f := &fakeFetcher{detail: github.FetchedPRDetail{
		Number: 42, Author: "carol",
		Reviews: []github.FetchedReview{
			{ID: "R1", Author: "", State: "APPROVED", Body: "empty author review"},
			{ID: "R2", Author: "alice", State: "APPROVED", Body: "real author review"},
		},
		Comments: []github.FetchedComment{
			{ID: "C1", Author: "", Body: "empty author comment"},
			{ID: "C2", Author: "bob", Body: "real author comment"},
		},
	}}
	h := New("sekret", f, st, scorer.Default())

	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	st.UpsertPerson(store.Person{Login: "alice", Team: "member", Active: true})
	st.UpsertPerson(store.Person{Login: "bob", Team: "member", Active: true})
	board, _ := st.Leaderboard("all", timeNowUTC())
	pts := map[string]int{}
	for _, r := range board {
		pts[r.Login] = r.Points
	}

	if pts["alice"] != 4 { // APPROVED (2+1) + message-bump (1)
		t.Errorf("alice points = %d, want 4", pts["alice"])
	}
	if pts["bob"] != 1 { // comment base = 1
		t.Errorf("bob points = %d, want 1", pts["bob"])
	}
	// Verify no empty-author rows exist; if they did, there would be a row with 0 points
	if len(board) != 2 {
		t.Errorf("leaderboard size = %d, want 2 (alice + bob, no empty-author rows)", len(board))
	}
}

func timeNowUTC() time.Time { return time.Now().UTC() }
