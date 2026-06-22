// Package config loads runtime configuration from env vars and projects.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"pr-review-dashboard/internal/scorer"
)

// Config is the resolved runtime configuration.
type Config struct {
	GitHubToken  string
	RosterTeam   string
	DBPath       string
	Repos        []string
	PollInterval time.Duration
	HealthPort   string
	Weights      scorer.Weights
}

type projectsFile struct {
	Projects map[string]json.RawMessage `json:"projects"`
}

// Load resolves configuration from env vars. Repos come from the REPOS env var
// (comma-separated "owner/name") if set; otherwise from the JSON file at
// projectsPath. ROSTER_TEAM ("org/team") must be supplied via env to enable
// roster sync.
func Load(projectsPath string) (Config, error) {
	c := Config{
		GitHubToken:  firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		RosterTeam:   os.Getenv("ROSTER_TEAM"),
		DBPath:       envOr("DB_PATH", "/data/leaderboard.db"),
		HealthPort:   envOr("HEALTH_PORT", "8080"),
		PollInterval: durationOr("POLL_INTERVAL", 15*time.Minute),
		Weights:      scorer.Default(),
	}
	// REPOS env var takes precedence over the projects file.
	if repos := parseRepos(os.Getenv("REPOS")); len(repos) > 0 {
		c.Repos = repos
		return c, nil
	}
	b, err := os.ReadFile(projectsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return c, fmt.Errorf("no repos configured: set the REPOS env var or provide %s", projectsPath)
		}
		return c, err
	}
	var pf projectsFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return c, err
	}
	for repo := range pf.Projects {
		c.Repos = append(c.Repos, repo)
	}
	sort.Strings(c.Repos)
	return c, nil
}

// parseRepos splits a comma-separated "owner/name" list, trimming blanks.
func parseRepos(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func durationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
