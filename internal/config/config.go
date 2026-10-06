// Package config loads lanes settings from TOML and resolves XDG paths.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Duration is a time.Duration that decodes from strings like "60s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	LinearPoll   Duration `toml:"linear_poll"`
	ExternalPoll Duration `toml:"external_poll"`
	Assignee     string   `toml:"assignee"`

	RepoRoots        []string `toml:"repo_roots"`
	DefaultAgent     string   `toml:"default_agent"`
	BranchTemplate   string   `toml:"branch_template"`
	WorktreeTemplate string   `toml:"worktree_template"`
	PromptTemplate   string   `toml:"prompt_template"`
	Preamble         string   `toml:"preamble"`
	NotifyOS         bool     `toml:"notify_os"` // also send desktop notifications

	Agents map[string]AgentConfig `toml:"agents"`
	Teams  map[string]TeamConfig  `toml:"teams"`
}

type AgentConfig struct {
	Args []string `toml:"args"`
}

type TeamConfig struct {
	// StateOrder lists workflow state names in display order; unlisted states follow.
	StateOrder []string `toml:"state_order"`
}

// Template placeholders: {key} {key_lower} {slug} {title} {url} {description}
// {preamble} {repo} {branch}.
const DefaultPrompt = `{preamble}

Ticket {key}: {title}
{url}

{description}`

func Default() Config {
	return Config{
		LinearPoll:       Duration{60 * time.Second},
		ExternalPoll:     Duration{5 * time.Second},
		Assignee:         "me",
		RepoRoots:        []string{"~/src", "~/git"},
		DefaultAgent:     "claude",
		BranchTemplate:   "{key_lower}/{slug}",
		WorktreeTemplate: "{repo}/.worktrees/{branch}",
		PromptTemplate:   DefaultPrompt,
	}
}

// Path returns $XDG_CONFIG_HOME/lanes/config.toml (default ~/.config/lanes/config.toml).
func Path() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "lanes", "config.toml")
}

// Load reads the config file at path; a missing file yields defaults.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) && len(de.Key()) > 0 {
			return c, fmt.Errorf("%s: %v: %w", path, de.Key(), err)
		}
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for key, d := range map[string]time.Duration{"linear_poll": c.LinearPoll.Duration, "external_poll": c.ExternalPoll.Duration} {
		if d < time.Second {
			return c, fmt.Errorf("%s: %s must be at least 1s, got %v", path, key, d)
		}
	}
	for key, v := range map[string]string{"default_agent": c.DefaultAgent, "worktree_template": c.WorktreeTemplate, "prompt_template": c.PromptTemplate} {
		if strings.TrimSpace(v) == "" {
			return c, fmt.Errorf("%s: %s must not be empty", path, key)
		}
	}
	if !strings.Contains(c.BranchTemplate, "{key}") && !strings.Contains(c.BranchTemplate, "{key_lower}") {
		return c, fmt.Errorf("%s: branch_template must contain {key} or {key_lower} so agents can be matched to tickets", path)
	}
	for i, r := range c.RepoRoots {
		c.RepoRoots[i] = Expand(r)
	}
	return c, nil
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// StateDir returns $XDG_STATE_HOME/lanes (default ~/.local/state/lanes), creating it 0700.
func StateDir() (string, error) {
	dir := filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "lanes")
	return dir, os.MkdirAll(dir, 0o700)
}

// xdg resolves an XDG base dir; relative values are ignored, as the spec requires.
func xdg(env, fallback string) string {
	if v := os.Getenv(env); filepath.IsAbs(v) {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}
