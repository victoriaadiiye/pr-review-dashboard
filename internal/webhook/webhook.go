// Package webhook receives GitHub pull_request events and, on merge, delegates
// scoring to the ingest package. Optional: enabled only when a secret is set.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/ingest"
)

// New returns the GitHub webhook handler. If secret is empty the route is
// disabled and returns 503. Scoring is delegated to ing.
func New(secret string, ing *ingest.Ingester) http.Handler {
	return &handler{secret: secret, ing: ing}
}

type handler struct {
	secret string
	ing    *ingest.Ingester
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
	owner, repo, ok := github.SplitRepo(ev.Repository.FullName)
	if !ok {
		http.Error(w, "bad repository.full_name", http.StatusBadRequest)
		return
	}
	if err := h.ing.ScorePR(r.Context(), ev.Repository.FullName, owner, repo, ev.PullRequest.Number); err != nil {
		log.Printf("webhook score %s#%d: %v", ev.Repository.FullName, ev.PullRequest.Number, err)
		http.Error(w, "scoring failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("scored"))
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
