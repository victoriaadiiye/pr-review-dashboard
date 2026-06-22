package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const prResponse = `{"data":{"repository":{"pullRequests":{"nodes":[
{"number":1,"title":"feat","url":"u","isDraft":false,
 "author":{"login":"bob"},
 "createdAt":"2026-06-10T10:00:00Z","updatedAt":"2026-06-10T11:00:00Z","mergedAt":null,
 "reviewRequests":{"nodes":[{"requestedReviewer":{"login":"alice"}}]},
 "reviews":{"nodes":[
   {"author":{"login":"alice"},"state":"COMMENTED","submittedAt":"2026-06-10T10:30:00Z","body":"nice","comments":{"totalCount":2}}
 ]}}
],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`

func TestFetchPullRequestsSurfacesGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errors":[{"message":"Bad credentials"}]}`))
	}))
	defer srv.Close()

	c := NewClient("bad-token").WithEndpoint(srv.URL)
	_, err := c.FetchPullRequests(context.Background(), "acme", "widgets")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error %q does not contain GraphQL error message", err.Error())
	}
}

func TestFetchPullRequestsParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(prResponse))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	prs, err := c.FetchPullRequests(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("prs = %d, want 1", len(prs))
	}
	p := prs[0]
	if p.Number != 1 || p.Author != "bob" || p.IsDraft {
		t.Errorf("pr = %+v", p)
	}
	if len(p.RequestedReviewers) != 1 || p.RequestedReviewers[0] != "alice" {
		t.Errorf("requested = %v", p.RequestedReviewers)
	}
	if len(p.Reviews) != 1 || p.Reviews[0].State != "COMMENTED" || p.Reviews[0].InlineComments != 2 {
		t.Errorf("review = %+v", p.Reviews)
	}
}
