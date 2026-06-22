// Command pr-review-dashboard polls GitHub for PR reviews, scores them, and
// serves a leaderboard + review-queue dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"pr-review-dashboard/internal/config"
	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/httpserver"
	"pr-review-dashboard/internal/poller"
	"pr-review-dashboard/internal/store"
)

func main() {
	cfg, err := config.Load("projects.json")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.GitHubToken == "" {
		log.Fatal("no GITHUB_TOKEN/GH_TOKEN set")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	p := poller.New(github.NewClient(cfg.GitHubToken), st, cfg.Weights)

	// Background sync loop.
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := p.SyncRoster(ctx, cfg.RosterTeam); err != nil {
				log.Printf("roster sync: %v", err)
			}
			for _, repo := range cfg.Repos {
				if err := p.SyncRepo(ctx, repo); err != nil {
					log.Printf("repo sync %s: %v", repo, err)
				}
			}
			cancel()
			time.Sleep(cfg.PollInterval)
		}
	}()

	h := httpserver.New(st, httpserver.Assets(), nil)
	addr := ":" + cfg.HealthPort
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		log.Fatalf("server: %v", err)
	}
}
