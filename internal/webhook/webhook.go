// Package webhook receives GitHub pull_request events and, on merge, scores the
// PR's reviews and comments into the store.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

// PRFetcher fetches a single PR's review + comment history (test seam).
type PRFetcher interface {
	FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error)
}

type handler struct {
	secret  string
	fetcher PRFetcher
	st      *store.Store
	weights scorer.Weights
}

// New returns the GitHub webhook handler. If secret is empty the route is
// disabled and returns 503, mirroring the digest-disabled pattern.
func New(secret string, fetcher PRFetcher, st *store.Store, w scorer.Weights) http.Handler {
	return &handler{secret: secret, fetcher: fetcher, st: st, weights: w}
}

type prEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int  `json:"number"`
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.secret == "" {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if !verifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var ev prEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if ev.Action != "closed" || !ev.PullRequest.Merged {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	owner, repo, ok := splitFullName(ev.Repository.FullName)
	if !ok {
		http.Error(w, "bad repository.full_name", http.StatusBadRequest)
		return
	}
	if err := h.score(r.Context(), ev.Repository.FullName, owner, repo, ev.PullRequest.Number); err != nil {
		log.Printf("webhook score %s#%d: %v", ev.Repository.FullName, ev.PullRequest.Number, err)
		http.Error(w, "scoring failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("scored"))
}

// score fetches the PR and upserts scored reviews + issue comments. fullName is
// the "owner/repo" string stored on each event row.
func (h *handler) score(ctx context.Context, fullName, owner, repo string, number int) error {
	d, err := h.fetcher.FetchPullRequest(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, rv := range d.Reviews {
		if rv.Author == "" {
			continue
		}
		hasImg := scorer.HasImage(rv.Body)
		pts := scorer.Score(scorer.Review{
			State: rv.State, InlineComments: rv.InlineComments, BodyLen: len(rv.Body),
			HasImage: hasImg, SelfReview: rv.Author == d.Author,
		}, h.weights)
		if err := h.st.UpsertReviewEvent(store.ReviewEvent{
			Repo: fullName, PRNumber: number, Reviewer: rv.Author, State: rv.State,
			InlineComments: rv.InlineComments, BodyLen: len(rv.Body), SubmittedAt: rv.SubmittedAt,
			Points: pts, HasImage: hasImg,
			RawHash: hashKey(fullName, number, rv.Author, "review", rv.ID, len(rv.Body)),
		}); err != nil {
			return fmt.Errorf("upsert review %s: %w", rv.ID, err)
		}
		if err := h.seedGuest(rv.Author); err != nil {
			return err
		}
	}
	for _, cm := range d.Comments {
		if cm.Author == "" {
			continue
		}
		hasImg := scorer.HasImage(cm.Body)
		pts := scorer.ScoreComment(scorer.Comment{
			BodyLen: len(cm.Body), HasImage: hasImg, SelfComment: cm.Author == d.Author,
		}, h.weights)
		if err := h.st.UpsertCommentEvent(store.CommentEvent{
			Repo: fullName, PRNumber: number, Author: cm.Author, Kind: "issue",
			BodyLen: len(cm.Body), HasImage: hasImg, CreatedAt: cm.CreatedAt, Points: pts,
			RawHash: hashKey(fullName, number, cm.Author, "issue", cm.ID, len(cm.Body)),
		}); err != nil {
			return fmt.Errorf("upsert comment %s: %w", cm.ID, err)
		}
		if err := h.seedGuest(cm.Author); err != nil {
			return err
		}
	}
	return nil
}

// seedGuest records an actor as a guest if not already on the roster. Never
// downgrades an existing member (EnsurePerson is insert-if-absent).
func (h *handler) seedGuest(login string) error {
	if login == "" {
		return nil
	}
	return h.st.EnsurePerson(store.Person{Login: login, DisplayName: login, Team: "guest", Active: true})
}

func verifySignature(secret string, body []byte, header string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header))
}

func splitFullName(s string) (string, string, bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// hashKey is the idempotency key for an event row. Including bodyLen means an
// edited body re-scores; the stable node id prevents double-counting redelivery.
func hashKey(repo string, pr int, actor, kind, id string, bodyLen int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, actor, kind, id, bodyLen)))
	return hex.EncodeToString(sum[:])
}
