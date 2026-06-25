package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/owenps/watchtower/internal/domain"
)

type Config struct {
	RefreshInterval string         `toml:"refresh_interval"`
	TerminalBell    *bool          `toml:"terminal_bell"`
	Repos           []Repo         `toml:"repos"`
	Actions         []ActionConfig `toml:"actions"`
}

type Repo struct {
	Name                       string `toml:"name"`
	Enabled                    *bool  `toml:"enabled"`
	WatchMyPRs                 *bool  `toml:"watch_my_prs"`
	WatchMyIssues              *bool  `toml:"watch_my_issues"`
	WatchAssignedIssues        *bool  `toml:"watch_assigned_issues"`
	WatchReviewPRs             *bool  `toml:"watch_review_prs"`
	WatchPRDescriptionThumbsUp *bool  `toml:"watch_pr_description_thumbs_up"`
}

type ActionConfig struct {
	Name      string   `toml:"name"`
	Label     string   `toml:"label"`
	AppliesTo []string `toml:"applies_to"`
	Risk      string   `toml:"risk"`
	Prompt    string   `toml:"prompt"`
	Command   string   `toml:"command"`
}

func Dir() (string, error) {
	if d := os.Getenv("WATCHTOWER_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "watchtower"), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

func StatePath() (string, error) {
	if p := os.Getenv("WATCHTOWER_STATE_PATH"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "watchtower", "state.db"), nil
}

func LoadOrCreate() (Config, string, error) {
	path, err := Path()
	if err != nil {
		return Config{}, "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Config{}, "", err
		}
		if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
			return Config{}, "", err
		}
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, path, err
	}
	cfg.ApplyDefaults()
	return cfg, path, nil
}

func (c *Config) ApplyDefaults() {
	if c.RefreshInterval == "" {
		c.RefreshInterval = "5m"
	}
	if c.TerminalBell == nil {
		c.TerminalBell = boolp(true)
	}
	for i := range c.Repos {
		if c.Repos[i].Enabled == nil {
			c.Repos[i].Enabled = boolp(true)
		}
		if c.Repos[i].WatchMyPRs == nil {
			c.Repos[i].WatchMyPRs = boolp(true)
		}
		if c.Repos[i].WatchMyIssues == nil {
			c.Repos[i].WatchMyIssues = boolp(true)
		}
		if c.Repos[i].WatchAssignedIssues == nil {
			c.Repos[i].WatchAssignedIssues = boolp(true)
		}
		if c.Repos[i].WatchReviewPRs == nil {
			c.Repos[i].WatchReviewPRs = boolp(false)
		}
		if c.Repos[i].WatchPRDescriptionThumbsUp == nil {
			c.Repos[i].WatchPRDescriptionThumbsUp = boolp(false)
		}
	}
	for i := range c.Actions {
		if c.Actions[i].Risk == "" {
			c.Actions[i].Risk = "read"
		}
		if c.Actions[i].Label == "" {
			c.Actions[i].Label = c.Actions[i].Name
		}
	}
}

func (c Config) RefreshDuration() time.Duration {
	if c.RefreshInterval == "off" {
		return 0
	}
	d, err := time.ParseDuration(c.RefreshInterval)
	if err != nil {
		return 5 * time.Minute
	}
	if d < time.Minute {
		return 0
	}
	return d.Truncate(time.Minute)
}

func (c Config) Save(path string) error {
	c.ApplyDefaults()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func (c Config) RepoRules() (map[string]domain.RepoRules, error) {
	rules := make(map[string]domain.RepoRules, len(c.Repos))
	for _, repo := range c.Repos {
		if repo.Name == "" {
			return nil, fmt.Errorf("repo name cannot be empty")
		}
		rules[repo.Name] = domain.RepoRules{
			Name:                       repo.Name,
			Enabled:                    value(repo.Enabled),
			WatchMyPRs:                 value(repo.WatchMyPRs),
			WatchMyIssues:              value(repo.WatchMyIssues),
			WatchAssignedIssues:        value(repo.WatchAssignedIssues),
			WatchReviewPRs:             value(repo.WatchReviewPRs),
			WatchPRDescriptionThumbsUp: value(repo.WatchPRDescriptionThumbsUp),
		}
	}
	return rules, nil
}

func boolp(v bool) *bool { return &v }

func value(v *bool) bool { return v != nil && *v }

const sampleConfig = `refresh_interval = "5m"
terminal_bell = true

# Add explicit repos. Review watch means: PRs not by you appear when ready for review.
# [[repos]]
# name = "owner/repo"
# enabled = true
# watch_my_prs = true
# watch_my_issues = true
# watch_assigned_issues = true
# watch_review_prs = false
# watch_pr_description_thumbs_up = false

[[actions]]
name = "summarize"
label = "Summarize"
applies_to = ["pr", "issue"]
risk = "read"
prompt = "Summarize why this needs attention in 5 short bullets."
command = "cat {{prompt_file}} {{context_file}}"
`
