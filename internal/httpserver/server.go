// Package httpserver exposes the JSON API and serves the embedded dashboard.
package httpserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"pr-review-dashboard/internal/store"
)

// New returns the HTTP handler. assets is the built Vue dashboard filesystem.
// runDigest triggers an on-demand Slack digest; pass nil to disable the route.
func New(st *store.Store, assets fs.FS, runDigest func(context.Context) error) http.Handler {
	mux := http.NewServeMux()

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
		writeJSON(w, rows, err)
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
