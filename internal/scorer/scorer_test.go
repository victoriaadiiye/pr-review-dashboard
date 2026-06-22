package scorer

import "testing"

func TestScore(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		r    Review
		want int
	}{
		{"bare approve", Review{State: "APPROVED", BodyLen: 3}, 3},                       // 2 base + 1 approved
		{"changes requested no comments", Review{State: "CHANGES_REQUESTED"}, 5},          // 2 + 3
		{"deep review", Review{State: "CHANGES_REQUESTED", InlineComments: 6, BodyLen: 400}, 13}, // 2+3+6+2
		{"inline cap", Review{State: "COMMENTED", InlineComments: 50, BodyLen: 0}, 14},     // 2+2+10(cap), no substance
		{"self review ignored", Review{State: "CHANGES_REQUESTED", SelfReview: true}, 0},
		{"substance threshold not met", Review{State: "APPROVED", BodyLen: 10}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(tt.r, w); got != tt.want {
				t.Errorf("Score(%+v) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}
