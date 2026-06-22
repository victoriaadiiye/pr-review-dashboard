package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsEnvAndProjects(t *testing.T) {
	dir := t.TempDir()
	pj := filepath.Join(dir, "projects.json")
	if err := os.WriteFile(pj, []byte(`{"projects":{"acme/widgets":{},"acme/gadgets":{}}}`), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("ROSTER_TEAM", "acme/reviewers")
	t.Setenv("POLL_INTERVAL", "5m")
	t.Setenv("REPOS", "") // force the projects.json path

	c, err := Load(pj)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.GitHubToken != "tok" || c.RosterTeam != "acme/reviewers" {
		t.Errorf("config = %+v", c)
	}
	if len(c.Repos) != 2 {
		t.Errorf("repos = %v", c.Repos)
	}
	if c.PollInterval.Minutes() != 5 {
		t.Errorf("interval = %v", c.PollInterval)
	}
}

func TestLoadReposFromEnvOverridesProjectsFile(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "acme/widgets, acme/gadgets ,")

	// No projects file on disk — REPOS must satisfy the config.
	c, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.Repos) != 2 || c.Repos[0] != "acme/gadgets" || c.Repos[1] != "acme/widgets" {
		t.Errorf("repos = %v, want sorted [acme/gadgets acme/widgets]", c.Repos)
	}
}

func TestLoadErrorsWhenNoReposConfigured(t *testing.T) {
	t.Setenv("REPOS", "")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected error when no REPOS and no projects file, got nil")
	}
}

func TestLoadDigestConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-1")
	t.Setenv("DIGEST_CHANNEL_ID", "C999")
	t.Setenv("STALE_PR_HOURS", "24")

	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SlackBotToken != "xoxb-1" {
		t.Errorf("SlackBotToken = %q", c.SlackBotToken)
	}
	if c.DigestChannelID != "C999" {
		t.Errorf("DigestChannelID = %q", c.DigestChannelID)
	}
	if c.StalePRHours != 24 {
		t.Errorf("StalePRHours = %v, want 24", c.StalePRHours)
	}
}

func TestLoadStalePRHoursDefault(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("REPOS", "a/b")
	t.Setenv("STALE_PR_HOURS", "")
	c, err := Load("does-not-exist.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.StalePRHours != 48 {
		t.Errorf("StalePRHours default = %v, want 48", c.StalePRHours)
	}
}
