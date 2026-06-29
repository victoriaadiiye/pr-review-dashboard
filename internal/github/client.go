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
	ID             string
	Author         string
	State          string
	Body           string
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
	Comments           []FetchedComment // issue comments, for "commented but not reviewed" status
	CommitDates        []time.Time      // committedDate of recent commits, for "new commits since review"
	Additions          int
	Deletions          int
	ChangedFiles       int
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
        number title url isDraft additions deletions changedFiles
        author{login}
        createdAt updatedAt mergedAt
        reviewRequests(first:20){nodes{requestedReviewer{... on User{login}}}}
        reviews(first:50){nodes{id author{login} state submittedAt body comments{totalCount}}}
        comments(first:50){nodes{author{login}}}
        commits(last:30){nodes{commit{committedDate}}}
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
					Number         int                     `json:"number"`
					Title          string                  `json:"title"`
					URL            string                  `json:"url"`
					IsDraft        bool                    `json:"isDraft"`
					Additions      int                     `json:"additions"`
					Deletions      int                     `json:"deletions"`
					ChangedFiles   int                     `json:"changedFiles"`
					Author         *struct{ Login string } `json:"author"`
					CreatedAt      time.Time               `json:"createdAt"`
					UpdatedAt      time.Time               `json:"updatedAt"`
					MergedAt       *time.Time              `json:"mergedAt"`
					ReviewRequests struct {
						Nodes []struct {
							RequestedReviewer *struct{ Login string } `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
					Reviews struct {
						Nodes []struct {
							ID          string                   `json:"id"`
							Author      *struct{ Login string }  `json:"author"`
							State       string                   `json:"state"`
							SubmittedAt *time.Time               `json:"submittedAt"`
							Body        string                   `json:"body"`
							Comments    struct{ TotalCount int } `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
					Comments struct {
						Nodes []struct {
							Author *struct{ Login string } `json:"author"`
						} `json:"nodes"`
					} `json:"comments"`
					Commits struct {
						Nodes []struct {
							Commit struct {
								CommittedDate *time.Time `json:"committedDate"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
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
			p.Additions = n.Additions
			p.Deletions = n.Deletions
			p.ChangedFiles = n.ChangedFiles
			if n.MergedAt != nil {
				p.MergedAt = *n.MergedAt
			}
			for _, rr := range n.ReviewRequests.Nodes {
				if rr.RequestedReviewer != nil && rr.RequestedReviewer.Login != "" {
					p.RequestedReviewers = append(p.RequestedReviewers, rr.RequestedReviewer.Login)
				}
			}
			for _, rv := range n.Reviews.Nodes {
				fr := FetchedReview{ID: rv.ID, Author: login(rv.Author), State: rv.State, Body: rv.Body, InlineComments: rv.Comments.TotalCount, BodyLen: len(rv.Body)}
				if rv.SubmittedAt != nil {
					fr.SubmittedAt = *rv.SubmittedAt
				}
				p.Reviews = append(p.Reviews, fr)
			}
			for _, c := range n.Comments.Nodes {
				p.Comments = append(p.Comments, FetchedComment{Author: login(c.Author)})
			}
			for _, c := range n.Commits.Nodes {
				if c.Commit.CommittedDate != nil {
					p.CommitDates = append(p.CommitDates, *c.Commit.CommittedDate)
				}
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

// FetchedComment is a parsed standalone PR issue comment.
type FetchedComment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
}

// FetchedPRDetail is one PR's full review + issue-comment history, for scoring
// at merge time.
type FetchedPRDetail struct {
	Number   int
	Author   string
	Title    string
	URL      string
	MergedAt *time.Time
	Reviews  []FetchedReview
	Comments []FetchedComment
}

const prDetailQuery = `
query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number
      title
      url
      mergedAt
      author{login}
      reviews(first:100){nodes{id author{login} state submittedAt body comments{totalCount}}}
      comments(first:100){nodes{id author{login} body createdAt}}
    }
  }
}`

type prDetailGQL struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Number   int                     `json:"number"`
				Title    string                  `json:"title"`
				URL      string                  `json:"url"`
				MergedAt *time.Time              `json:"mergedAt"`
				Author   *struct{ Login string } `json:"author"`
				Reviews  struct {
					Nodes []struct {
						ID          string                   `json:"id"`
						Author      *struct{ Login string }  `json:"author"`
						State       string                   `json:"state"`
						SubmittedAt *time.Time               `json:"submittedAt"`
						Body        string                   `json:"body"`
						Comments    struct{ TotalCount int } `json:"comments"`
					} `json:"nodes"`
				} `json:"reviews"`
				Comments struct {
					Nodes []struct {
						ID        string                  `json:"id"`
						Author    *struct{ Login string } `json:"author"`
						Body      string                  `json:"body"`
						CreatedAt time.Time               `json:"createdAt"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchPullRequest returns one PR's full review + issue-comment history. Used by
// the merge webhook to score a single known PR rather than scanning open PRs.
func (c *Client) FetchPullRequest(ctx context.Context, owner, repo string, number int) (FetchedPRDetail, error) {
	var resp prDetailGQL
	vars := map[string]any{"owner": owner, "repo": repo, "number": number}
	if err := c.do(ctx, prDetailQuery, vars, &resp); err != nil {
		return FetchedPRDetail{}, err
	}
	pr := resp.Data.Repository.PullRequest
	d := FetchedPRDetail{
		Number: pr.Number, Author: login(pr.Author),
		Title: pr.Title, URL: pr.URL, MergedAt: pr.MergedAt,
	}
	for _, rv := range pr.Reviews.Nodes {
		fr := FetchedReview{
			ID: rv.ID, Author: login(rv.Author), State: rv.State, Body: rv.Body,
			InlineComments: rv.Comments.TotalCount, BodyLen: len(rv.Body),
		}
		if rv.SubmittedAt != nil {
			fr.SubmittedAt = *rv.SubmittedAt
		}
		d.Reviews = append(d.Reviews, fr)
	}
	for _, cm := range pr.Comments.Nodes {
		d.Comments = append(d.Comments, FetchedComment{
			ID: cm.ID, Author: login(cm.Author), Body: cm.Body, CreatedAt: cm.CreatedAt,
		})
	}
	return d, nil
}

const mergedPRQuery = `
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:MERGED,first:50,after:$cursor,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{ number mergedAt updatedAt }
      pageInfo{ hasNextPage endCursor }
    }
  }
}`

type mergedPRGQL struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number    int        `json:"number"`
					MergedAt  *time.Time `json:"mergedAt"`
					UpdatedAt time.Time  `json:"updatedAt"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchMergedPRNumbers returns the numbers of PRs merged on/after since,
// newest-first. It pages pullRequests(states:MERGED) ordered by UPDATED_AT desc,
// collecting numbers whose mergedAt >= since, and stops paging once a node's
// updatedAt < since (safe because updatedAt >= mergedAt for any PR).
func (c *Client) FetchMergedPRNumbers(ctx context.Context, owner, repo string, since time.Time) ([]int, error) {
	var out []int
	var cursor *string
	for {
		var resp mergedPRGQL
		vars := map[string]any{"owner": owner, "repo": repo, "cursor": cursor}
		if err := c.do(ctx, mergedPRQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Data.Repository.PullRequests.Nodes {
			if n.UpdatedAt.Before(since) {
				return out, nil // all remaining are older; stop
			}
			if n.MergedAt != nil && !n.MergedAt.Before(since) {
				out = append(out, n.Number)
			}
		}
		pi := resp.Data.Repository.PullRequests.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		cursor = pi.EndCursor
	}
	return out, nil
}

// SplitRepo splits "owner/name" into its parts. ok is false if malformed.
func SplitRepo(full string) (owner, name string, ok bool) {
	i := strings.IndexByte(full, '/')
	if i <= 0 || i == len(full)-1 {
		return "", "", false
	}
	return full[:i], full[i+1:], true
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
