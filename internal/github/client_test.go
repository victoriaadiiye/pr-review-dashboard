package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestFetchPullRequest(t *testing.T) {
	const body = `{"data":{"repository":{"pullRequest":{
		"number":42,
		"author":{"login":"carol"},
		"reviews":{"nodes":[
			{"id":"R1","author":{"login":"alice"},"state":"CHANGES_REQUESTED","submittedAt":"2026-06-20T10:00:00Z","body":"please fix","comments":{"totalCount":3}}
		]},
		"comments":{"nodes":[
			{"id":"C1","author":{"login":"bob"},"body":"nice work","createdAt":"2026-06-20T11:00:00Z"}
		]}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	d, err := c.FetchPullRequest(context.Background(), "acme", "widgets", 42)
	if err != nil {
		t.Fatalf("FetchPullRequest: %v", err)
	}
	if d.Number != 42 || d.Author != "carol" {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Reviews) != 1 || d.Reviews[0].Author != "alice" || d.Reviews[0].ID != "R1" ||
		d.Reviews[0].Body != "please fix" || d.Reviews[0].InlineComments != 3 {
		t.Errorf("reviews = %+v", d.Reviews)
	}
	if len(d.Comments) != 1 || d.Comments[0].Author != "bob" || d.Comments[0].ID != "C1" || d.Comments[0].Body != "nice work" {
		t.Errorf("comments = %+v", d.Comments)
	}
}

func TestFetchMergedPRNumbers(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Node A: merged & updated after since -> included.
	// Node B: updated after since but merged before since -> excluded, keep going.
	// Node C: updated before since -> stop (and excluded).
	body := `{"data":{"repository":{"pullRequests":{
		"nodes":[
			{"number":50,"mergedAt":"2026-06-10T10:00:00Z","updatedAt":"2026-06-10T11:00:00Z"},
			{"number":40,"mergedAt":"2026-05-01T10:00:00Z","updatedAt":"2026-06-05T09:00:00Z"},
			{"number":30,"mergedAt":"2026-04-01T10:00:00Z","updatedAt":"2026-05-20T09:00:00Z"}
		],
		"pageInfo":{"hasNextPage":false,"endCursor":null}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	got, err := c.FetchMergedPRNumbers(context.Background(), "acme", "widgets", since)
	if err != nil {
		t.Fatalf("FetchMergedPRNumbers: %v", err)
	}
	if len(got) != 1 || got[0] != 50 {
		t.Errorf("got %v, want [50] (40 merged before since; 30 stops the scan)", got)
	}
}

func TestFetchPullRequestsParsesSize(t *testing.T) {
	const body = `{"data":{"repository":{"pullRequests":{
		"nodes":[{
			"number":7,"title":"t","url":"u","isDraft":false,
			"author":{"login":"alice"},
			"createdAt":"2026-06-20T10:00:00Z","updatedAt":"2026-06-21T10:00:00Z","mergedAt":null,
			"additions":210,"deletions":18,"changedFiles":4,
			"reviewRequests":{"nodes":[]},
			"reviews":{"nodes":[]}
		}],
		"pageInfo":{"hasNextPage":false,"endCursor":null}
	}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("tok").WithEndpoint(srv.URL)
	prs, err := c.FetchPullRequests(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(prs))
	}
	if prs[0].Additions != 210 || prs[0].Deletions != 18 || prs[0].ChangedFiles != 4 {
		t.Errorf("size = +%d -%d / %d files, want +210 -18 / 4", prs[0].Additions, prs[0].Deletions, prs[0].ChangedFiles)
	}
}

func TestSplitRepo(t *testing.T) {
	cases := []struct {
		in          string
		owner, name string
		ok          bool
	}{
		{"acme/widgets", "acme", "widgets", true},
		{"noslash", "", "", false},
		{"/leading", "", "", false},
		{"trailing/", "", "", false},
	}
	for _, c := range cases {
		o, n, ok := SplitRepo(c.in)
		if o != c.owner || n != c.name || ok != c.ok {
			t.Errorf("SplitRepo(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, o, n, ok, c.owner, c.name, c.ok)
		}
	}
}
