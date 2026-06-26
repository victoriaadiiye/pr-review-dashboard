// Package ingest scores a single merged PR's reviews and comments into the
// store. It is the shared scoring pass used by both the merge scanner and the
// optional webhook, so scoring cannot diverge between triggers.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/scorer"
	"pr-review-dashboard/internal/store"
)

// PRFetcher fetches a single PR's review + comment history (test seam).
type PRFetcher interface {
	FetchPullRequest(ctx context.Context, owner, repo string, number int) (github.FetchedPRDetail, error)
}

// Ingester scores merged PRs into the store.
type Ingester struct {
	fetcher PRFetcher
	st      *store.Store
	weights scorer.Weights
}

// New constructs an Ingester.
func New(fetcher PRFetcher, st *store.Store, w scorer.Weights) *Ingester {
	return &Ingester{fetcher: fetcher, st: st, weights: w}
}

// ScorePR fetches one PR's reviews + issue comments, scores each, and upserts
// them. fullName is the "owner/repo" string stored on each event row; owner and
// repo are its split halves used for the fetch. Idempotent via raw_hash.
// Self-authored events score 0 but are still stored; empty-author
// (deleted-account) events are skipped entirely.
func (i *Ingester) ScorePR(ctx context.Context, fullName, owner, repo string, number int) error {
	d, err := i.fetcher.FetchPullRequest(ctx, owner, repo, number)
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
		}, i.weights)
		if err := i.st.UpsertReviewEvent(store.ReviewEvent{
			Repo: fullName, PRNumber: number, Reviewer: rv.Author, State: rv.State,
			InlineComments: rv.InlineComments, BodyLen: len(rv.Body), SubmittedAt: rv.SubmittedAt,
			Points: pts, HasImage: hasImg,
			RawHash: hashKey(fullName, number, rv.Author, "review", rv.ID, len(rv.Body)),
		}); err != nil {
			return fmt.Errorf("upsert review %s: %w", rv.ID, err)
		}
		if err := i.seedGuest(rv.Author); err != nil {
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
		}, i.weights)
		if err := i.st.UpsertCommentEvent(store.CommentEvent{
			Repo: fullName, PRNumber: number, Author: cm.Author, Kind: "issue",
			BodyLen: len(cm.Body), HasImage: hasImg, CreatedAt: cm.CreatedAt, Points: pts,
			RawHash: hashKey(fullName, number, cm.Author, "issue", cm.ID, len(cm.Body)),
		}); err != nil {
			return fmt.Errorf("upsert comment %s: %w", cm.ID, err)
		}
		if err := i.seedGuest(cm.Author); err != nil {
			return err
		}
	}
	return nil
}

// seedGuest records an actor as a guest if not already on the roster. Never
// downgrades an existing member (EnsurePerson is insert-if-absent).
func (i *Ingester) seedGuest(login string) error {
	if login == "" {
		return nil
	}
	return i.st.EnsurePerson(store.Person{Login: login, DisplayName: login, Team: "guest", Active: true})
}

// hashKey is the idempotency key for an event row. Including bodyLen means an
// edited body re-scores; the stable node id prevents double-counting redelivery.
func hashKey(repo string, pr int, actor, kind, id string, bodyLen int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d", repo, pr, actor, kind, id, bodyLen)))
	return hex.EncodeToString(sum[:])
}
