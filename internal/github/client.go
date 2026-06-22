// Package github is a minimal GitHub GraphQL client for PR + review + team data.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultEndpoint = "https://api.github.com/graphql"

// Client talks to the GitHub GraphQL API.
type Client struct {
	token      string
	endpoint   string
	httpClient *http.Client
}

// NewClient returns a client authenticating with the given token.
func NewClient(token string) *Client {
	return &Client{token: token, endpoint: defaultEndpoint, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

// WithEndpoint overrides the GraphQL endpoint (used in tests).
func (c *Client) WithEndpoint(url string) *Client { c.endpoint = url; return c }

// FetchedReview is a parsed review.
type FetchedReview struct {
	Author         string
	State          string
	InlineComments int
	BodyLen        int
	SubmittedAt    time.Time
}

// FetchedPR is a parsed pull request with its reviews.
type FetchedPR struct {
	Number             int
	Title              string
	Author             string
	URL                string
	IsDraft            bool
	ReadyAt            time.Time
	MergedAt           time.Time
	UpdatedAt          time.Time
	RequestedReviewers []string
	Reviews            []FetchedReview
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github graphql: read body: %w", err)
	}
	// Check for top-level GraphQL errors (returned with HTTP 200 by GitHub).
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("github graphql: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(raw, out)
}

const prQuery = `
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:OPEN,first:50,after:$cursor,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{
        number title url isDraft
        author{login}
        createdAt updatedAt mergedAt
        reviewRequests(first:20){nodes{requestedReviewer{... on User{login}}}}
        reviews(first:50){nodes{author{login} state submittedAt body comments{totalCount}}}
      }
      pageInfo{hasNextPage endCursor}
    }
  }
}`

type prGQL struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number    int    `json:"number"`
					Title     string `json:"title"`
					URL       string `json:"url"`
					IsDraft   bool   `json:"isDraft"`
					Author    *struct{ Login string } `json:"author"`
					CreatedAt time.Time  `json:"createdAt"`
					UpdatedAt time.Time  `json:"updatedAt"`
					MergedAt  *time.Time `json:"mergedAt"`
					ReviewRequests struct {
						Nodes []struct {
							RequestedReviewer *struct{ Login string } `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
					Reviews struct {
						Nodes []struct {
							Author      *struct{ Login string } `json:"author"`
							State       string     `json:"state"`
							SubmittedAt *time.Time `json:"submittedAt"`
							Body        string     `json:"body"`
							Comments    struct{ TotalCount int } `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchPullRequests returns all open PRs for owner/repo with their reviews.
func (c *Client) FetchPullRequests(ctx context.Context, owner, repo string) ([]FetchedPR, error) {
	var out []FetchedPR
	var cursor *string
	for {
		var resp prGQL
		vars := map[string]any{"owner": owner, "repo": repo, "cursor": cursor}
		if err := c.do(ctx, prQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Data.Repository.PullRequests.Nodes {
			p := FetchedPR{
				Number: n.Number, Title: n.Title, URL: n.URL, IsDraft: n.IsDraft,
				ReadyAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
			}
			p.Author = login(n.Author)
			if n.MergedAt != nil {
				p.MergedAt = *n.MergedAt
			}
			for _, rr := range n.ReviewRequests.Nodes {
				if rr.RequestedReviewer != nil && rr.RequestedReviewer.Login != "" {
					p.RequestedReviewers = append(p.RequestedReviewers, rr.RequestedReviewer.Login)
				}
			}
			for _, rv := range n.Reviews.Nodes {
				fr := FetchedReview{Author: login(rv.Author), State: rv.State, InlineComments: rv.Comments.TotalCount, BodyLen: len(rv.Body)}
				if rv.SubmittedAt != nil {
					fr.SubmittedAt = *rv.SubmittedAt
				}
				p.Reviews = append(p.Reviews, fr)
			}
			out = append(out, p)
		}
		pi := resp.Data.Repository.PullRequests.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}

func login(a *struct{ Login string }) string {
	if a == nil {
		return ""
	}
	return a.Login
}

const teamQuery = `
query($org:String!,$team:String!,$cursor:String){
  organization(login:$org){
    team(slug:$team){
      members(first:100,after:$cursor){
        nodes{login}
        pageInfo{hasNextPage endCursor}
      }
    }
  }
}`

type teamGQL struct {
	Data struct {
		Organization struct {
			Team struct {
				Members struct {
					Nodes    []struct{ Login string } `json:"nodes"`
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"members"`
			} `json:"team"`
		} `json:"organization"`
	} `json:"data"`
}

// TeamMembers returns the logins of every member of org/team.
func (c *Client) TeamMembers(ctx context.Context, org, team string) ([]string, error) {
	var out []string
	var cursor *string
	for {
		var resp teamGQL
		vars := map[string]any{"org": org, "team": team, "cursor": cursor}
		if err := c.do(ctx, teamQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data.Organization.Team.Members.Nodes {
			out = append(out, m.Login)
		}
		pi := resp.Data.Organization.Team.Members.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}
