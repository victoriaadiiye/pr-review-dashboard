// Package scorer turns a submitted review into points. Pure, no I/O.
package scorer

// Weights configures the point values. See Default for the v1 baseline.
type Weights struct {
	Base           int
	Changes        int
	Commented      int
	Approved       int
	PerInline      int
	InlineCap      int
	Substance      int
	SubstanceChars int
}

// Default returns the v1 baseline weights from the spec.
func Default() Weights {
	return Weights{
		Base: 2, Changes: 3, Commented: 2, Approved: 1,
		PerInline: 1, InlineCap: 10, Substance: 2, SubstanceChars: 280,
	}
}

// Review is the scoreable shape of a submitted review.
type Review struct {
	State          string // APPROVED | CHANGES_REQUESTED | COMMENTED
	InlineComments int
	BodyLen        int // length of review body + inline comment text
	SelfReview     bool
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
	if r.BodyLen > w.SubstanceChars {
		pts += w.Substance
	}
	return pts
}
