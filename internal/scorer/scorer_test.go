package scorer

import "testing"

const longBody = 300 // > Default().SubstanceChars (280)

func TestScore(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		r    Review
		want int
	}{
		{"bare approve no message", Review{State: "APPROVED", BodyLen: 0}, 3},                                                                    // 2+1
		{"approve short message", Review{State: "APPROVED", BodyLen: 50}, 4},                                                                     // 2+1+1
		{"comment-only short message", Review{State: "COMMENTED", BodyLen: 50}, 5},                                                               // 2+2+1
		{"changes short message", Review{State: "CHANGES_REQUESTED", BodyLen: 50}, 6},                                                            // 2+3+1
		{"changes long no image", Review{State: "CHANGES_REQUESTED", BodyLen: longBody}, 7},                                                      // 2+3+2
		{"approve long + screenshot", Review{State: "APPROVED", BodyLen: longBody, HasImage: true}, 10},                                          // 2+1+2+5
		{"changes long + 5 inline", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, InlineComments: 5}, 12},                                // 2+3+2+5
		{"changes long + screenshot", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, HasImage: true}, 12},                                 // 2+3+2+5
		{"changes long + 10 inline + screenshot", Review{State: "CHANGES_REQUESTED", BodyLen: longBody, InlineComments: 10, HasImage: true}, 22}, // 2+3+2+10+5
		{"inline cap", Review{State: "COMMENTED", InlineComments: 50, BodyLen: 0}, 14},                                                           // 2+2+10(cap)
		{"self review ignored", Review{State: "CHANGES_REQUESTED", SelfReview: true}, 0},
		{"short body image gets no bonus", Review{State: "APPROVED", BodyLen: 50, HasImage: true}, 4}, // image gated on substance
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(tt.r, w); got != tt.want {
				t.Errorf("Score(%+v) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}

func TestScoreComment(t *testing.T) {
	w := Default()
	tests := []struct {
		name string
		c    Comment
		want int
	}{
		{"plain chat comment", Comment{BodyLen: 40}, 1},
		{"long comment + screenshot", Comment{BodyLen: longBody, HasImage: true}, 6}, // 1+5
		{"short comment + image no bonus", Comment{BodyLen: 40, HasImage: true}, 1},  // gated on substance
		{"self comment ignored", Comment{BodyLen: 40, SelfComment: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScoreComment(tt.c, w); got != tt.want {
				t.Errorf("ScoreComment(%+v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

func TestHasImage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"markdown image", "see ![shot](https://x/y.png) here", true},
		{"user-images host", "proof https://user-images.githubusercontent.com/1/2.png", true},
		{"user-attachments path", "https://github.com/user-attachments/assets/abc", true},
		{"plain text", "looks good to me, no screenshot", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasImage(tt.body); got != tt.want {
				t.Errorf("HasImage(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
