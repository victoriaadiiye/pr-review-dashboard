// Package httpserver exposes the JSON API and serves the embedded dashboard.
package httpserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"time"

	"pr-review-dashboard/internal/store"
)

// New returns the HTTP handler. assets is the built Vue dashboard filesystem.
// runDigest triggers an on-demand Slack digest; pass nil to disable the route.
// webhook handles POST /webhook/github; pass nil to disable the route.
// rosterSlug is the roster team's slug, used to resolve "a team you're on is
// requested" when the queue is personalized via ?me=.
// runSync forces an immediate GitHub sync (POST /api/sync); pass nil to disable.
func New(st *store.Store, assets fs.FS, runDigest func(context.Context) error, webhook http.Handler, staleHours float64, rosterSlug string, runSync func(context.Context) error) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/webhook/github", func(w http.ResponseWriter, r *http.Request) {
		if webhook == nil {
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}
		webhook.ServeHTTP(w, r)
	})

	mux.HandleFunc("/digest/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if runDigest == nil {
			http.Error(w, "digest not configured", http.StatusServiceUnavailable)
			return
		}
		if err := runDigest(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("digest sent"))
	})

	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if runSync == nil {
			http.Error(w, "sync not configured", http.StatusServiceUnavailable)
			return
		}
		if err := runSync(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("synced"))
	})

	mux.HandleFunc("/api/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		window := r.URL.Query().Get("window")
		if window == "" {
			window = "week"
		}
		rows, err := st.Leaderboard(window, time.Now())
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.Queue(time.Now())
		if err == nil {
			rows = store.RankQueue(rows, staleHours)
			if me := r.URL.Query().Get("me"); me != "" {
				team, _ := st.PersonTeam(me)
				store.AssignQueueRelations(rows, me, team == "member", rosterSlug)
			}
		}
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		window := r.URL.Query().Get("window")
		if window == "" {
			window = "all"
		}
		reviewer := r.URL.Query().Get("reviewer")
		rows, err := st.ReviewHistory(window, reviewer, time.Now())
		writeJSON(w, rows, err)
	})

	mux.HandleFunc("/api/reviewers", func(w http.ResponseWriter, r *http.Request) {
		who, err := st.DistinctReviewers()
		if err == nil {
			sort.Strings(who)
		}
		writeJSON(w, who, err)
	})

	mux.HandleFunc("/api/people", func(w http.ResponseWriter, r *http.Request) {
		people, err := st.People()
		writeJSON(w, people, err)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().UTC()}, nil)
	})

	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if v == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(v)
}
