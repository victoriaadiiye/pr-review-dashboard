// Package scorer turns a submitted review or comment into points. Pure, no I/O.
package scorer

import "strings"

// Weights configures the point values. See Default for the v2 baseline.
type Weights struct {
	Base           int
	Changes        int
	Commented      int
	Approved       int
	PerInline      int
	InlineCap      int
	Substance      int
	SubstanceChars int
	ImageBonus     int // bonus for a testing-proof image, gated on substance
	MessageBump    int // bonus for a non-empty short body (<= SubstanceChars)
	CommentBase    int // flat points for a standalone comment
}

// Default returns the v2 baseline weights from the spec.
func Default() Weights {
	return Weights{
		Base: 2, Changes: 3, Commented: 2, Approved: 1,
		PerInline: 1, InlineCap: 10, Substance: 2, SubstanceChars: 280,
		ImageBonus: 5, MessageBump: 1, CommentBase: 1,
	}
}

// Review is the scoreable shape of a submitted review.
type Review struct {
	State          string // APPROVED | CHANGES_REQUESTED | COMMENTED
	InlineComments int
	BodyLen        int // length of the review body
	HasImage       bool
	SelfReview     bool
}

// Comment is the scoreable shape of a standalone PR comment.
type Comment struct {
	BodyLen     int
	HasImage    bool
	SelfComment bool
}

// Score returns the points awarded for a review.
func Score(r Review, w Weights) int {
	if r.SelfReview {
		return 0
	}
	pts := w.Base
	switch r.State {
	case "CHANGES_REQUESTED":
		pts += w.Changes
	case "COMMENTED":
		pts += w.Commented
	case "APPROVED":
		pts += w.Approved
	}
	inline := r.InlineComments
	if inline > w.InlineCap {
		inline = w.InlineCap
	}
	pts += inline * w.PerInline
	pts += bodyAndImage(r.BodyLen, r.HasImage, w)
	return pts
}

// ScoreComment returns the points awarded for a standalone comment.
func ScoreComment(c Comment, w Weights) int {
	if c.SelfComment {
		return 0
	}
	pts := w.CommentBase
	if c.HasImage && c.BodyLen > w.SubstanceChars {
		pts += w.ImageBonus
	}
	return pts
}

// bodyAndImage adds the mutually-exclusive message-bump/substance points plus
// the substance-gated image bonus.
func bodyAndImage(bodyLen int, hasImage bool, w Weights) int {
	pts := 0
	switch {
	case bodyLen > w.SubstanceChars:
		pts += w.Substance
	case bodyLen > 0:
		pts += w.MessageBump
	}
	if hasImage && bodyLen > w.SubstanceChars {
		pts += w.ImageBonus
	}
	return pts
}

// HasImage reports whether body embeds an image or GitHub attachment.
func HasImage(body string) bool {
	if i := strings.Index(body, "!["); i >= 0 {
		if j := strings.Index(body[i:], "]("); j >= 0 && strings.Index(body[i+j:], ")") >= 0 {
			return true
		}
	}
	return strings.Contains(body, "user-images.githubusercontent.com") ||
		strings.Contains(body, "github.com/user-attachments/")
}
