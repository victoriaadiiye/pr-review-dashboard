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

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/ingest"
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

func newHandler(t *testing.T, secret string, f *fakeFetcher) http.Handler {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(secret, ingest.New(f, st, scorer.Default()))
}

func TestMergedEventDelegatesToIngest(t *testing.T) {
	f := &fakeFetcher{detail: github.FetchedPRDetail{Number: 42, Author: "carol"}}
	h := newHandler(t, "sekret", f)
	body := []byte(mergedBody)
	rec := post(t, h, "pull_request", sign("sekret", body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (delegated to ingest)", f.calls)
	}
}

func TestBadSignatureRejected(t *testing.T) {
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "sha256=deadbeef", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMissingSignatureRejected(t *testing.T) {
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDisabledWhenNoSecret(t *testing.T) {
	h := newHandler(t, "", &fakeFetcher{})
	body := []byte(mergedBody)
	if rec := post(t, h, "pull_request", "", body); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNonMergeEventIgnored(t *testing.T) {
	f := &fakeFetcher{}
	h := newHandler(t, "sekret", f)
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
	h := newHandler(t, "sekret", &fakeFetcher{})
	body := []byte(`{}`)
	if rec := post(t, h, "push", sign("sekret", body), body); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
