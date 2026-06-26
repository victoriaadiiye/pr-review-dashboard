// Command pr-review-dashboard polls GitHub for PR reviews, scores them, and
// serves a leaderboard + review-queue dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"pr-review-dashboard/internal/config"
	"pr-review-dashboard/internal/digest"
	"pr-review-dashboard/internal/github"
	"pr-review-dashboard/internal/httpserver"
	"pr-review-dashboard/internal/poller"
	"pr-review-dashboard/internal/store"
	"pr-review-dashboard/internal/webhook"
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

	p := poller.New(github.NewClient(cfg.GitHubToken), st)

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

	// Slack digest: enabled only when a bot token and channel are configured.
	var runDigest func(context.Context) error
	if cfg.SlackBotToken != "" && cfg.DigestChannelID != "" {
		dg := digest.New(st, digest.NewSlackClient(cfg.SlackBotToken), cfg.DigestChannelID, cfg.StalePRHours)
		runDigest = func(ctx context.Context) error { return dg.Run(ctx, time.Now()) }
		go dg.RunScheduler(context.Background(), time.Now)
		log.Printf("digest scheduler enabled for channel %s (09:00 Europe/Dublin)", cfg.DigestChannelID)
	} else {
		log.Print("digest disabled: set SLACK_BOT_TOKEN and DIGEST_CHANNEL_ID to enable")
	}

	// GitHub merge webhook: enabled only when a secret is configured.
	var webhookHandler http.Handler
	if cfg.WebhookSecret != "" {
		webhookHandler = webhook.New(cfg.WebhookSecret, github.NewClient(cfg.GitHubToken), st, cfg.Weights)
		log.Print("webhook enabled at POST /webhook/github")
	} else {
		log.Print("webhook disabled: set WEBHOOK_SECRET to enable")
	}

	h := httpserver.New(st, httpserver.Assets(), runDigest, webhookHandler)
	addr := ":" + cfg.HealthPort
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		log.Fatalf("server: %v", err)
	}
}
