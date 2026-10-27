// Package config loads lanes settings from TOML and resolves XDG paths.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	LaunchDir        string   `toml:"launch_dir"` // start new agents in this folder (no worktree); "" = a worktree per ticket
	DefaultAgent     string   `toml:"default_agent"`
	BranchTemplate   string   `toml:"branch_template"`
	WorktreeTemplate string   `toml:"worktree_template"`
	PromptTemplate   string   `toml:"prompt_template"`
	Preamble         string   `toml:"preamble"`
	NotifyOS         bool     `toml:"notify_os"`        // also send desktop notifications
	AutoLink         bool     `toml:"auto_link"`        // link new sessions whose transcript clearly points at one ticket
	EndedDays        int      `toml:"ended_days"`       // how long ended sessions stay under their ticket (0 = not at all)
	GroupByProject   bool     `toml:"group_by_project"` // within a lane, group tickets by project
	NotifyVia        string   `toml:"notify_via"`       // desktop notifications: "auto", "terminal" (escape code), "system"
	DimInactive      bool     `toml:"dim_inactive"`     // fade the pane (board or agent) you're not in
	FocusKey         string   `toml:"focus_key"`        // optional tmux key that jumps between board and agent ("" = use the tmux prefix + h/l or arrows)

	Agents      map[string]AgentConfig `toml:"agents"`
	Teams       map[string]TeamConfig  `toml:"teams"`
	AskAgent    string                 `toml:"ask_agent"` // the tool that answers : and drafts N tickets; "" = the default agent if it can
	Coordinator CoordinatorConfig      `toml:"coordinator"`
	Keys        map[string]string      `toml:"keys"` // board action → key; see KeyActions
	Review      ReviewConfig           `toml:"review"`
}

// ReviewConfig is how a pull request review agent (R) works.
type ReviewConfig struct {
	Agent string `toml:"agent"` // the tool it runs in; "" = the default agent
	// Prompt is its first prompt: {url}, {repo} (owner/name), {number}, {title}, and
	// {post} (what it may post, from Post) are filled in.
	Prompt string `toml:"prompt"`
	// Post: "ask" (it posts only after the user says yes), "never" (it drafts; the user
	// posts), "auto" (it posts its review when done).
	Post string `toml:"post"`
	// FindTools are the only tools the agent that finds a PR from a description ("latest PR
	// from sam in #code-reviews") may use: read-only ones. Rules as the tool writes them.
	FindTools []string `toml:"find_tools"`
}

// DefaultFindTools read Slack (under the names Claude's Slack connections use) and GitHub.
var DefaultFindTools = []string{
	"mcp__claude_ai_Slack__slack_search_*", "mcp__claude_ai_Slack__slack_read_*",
	"mcp__plugin_slack_slack__slack_search_*", "mcp__plugin_slack_slack__slack_read_*",
	"Bash(gh pr view:*)", "Bash(gh pr list:*)", "Bash(gh search prs:*)",
}

// DefaultReviewPrompt is the review agent's first prompt unless review.prompt is set.
const DefaultReviewPrompt = `Review the pull request {url} ({repo}#{number}: {title}). Its head is checked out here.

Read the change (gh pr view {number} --repo {repo}, gh pr diff {number} --repo {repo}) and the code around it. Look for bugs, risky changes, security problems, and missing tests; say how sure you are of each, and keep style nits apart from real problems.

{post}`

// ReviewPost is what a review agent is told it may post, by review.post.
var ReviewPost = map[string]string{
	"ask":   "Write your review here first. Post it to GitHub (gh pr review) only after the user says to.",
	"never": "Don't post anything to GitHub: write your review here, and the user will post what they want.",
	"auto":  "When you're done, post your review to GitHub as a comment review, with exactly: gh pr review {number} --repo {repo} --comment --body-file <file>.",
}

// CoordinatorConfig is what a project's coordinator may do without the panel.
type CoordinatorConfig struct {
	// Agent is the tool coordinators run in; "" = the default agent if it can.
	Agent string `toml:"agent"`
	// Start: "ask" (Claude asks you in the coordinator's pane before each start), "auto"
	// (it starts agents without asking), "panel" (you confirm each start in the panel).
	Start string `toml:"start"`
	// Approve: "ask" (each allow of an agent's prompt asks you in the coordinator's
	// pane), "auto" (it answers agents' prompts itself), "off" (it can't answer them).
	Approve string `toml:"approve"`
}

type AgentConfig struct {
	Args []string `toml:"args"`
	// For a tool lanes has no built-in support for: how to run it ([agents.<name>] with
	// command set adds it). The first prompt goes last on the command line, after
	// prompt_flag if set; stop is typed (then Enter) to make it exit, else stop_keys
	// (tmux key names) are pressed; waiting is text its pane shows when it needs you.
	Command    []string `toml:"command"`
	PromptFlag string   `toml:"prompt_flag"`
	Stop       string   `toml:"stop"`
	StopKeys   []string `toml:"stop_keys"`
	Waiting    []string `toml:"waiting"`
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

{description}

{siblings}`

func Default() Config {
	return Config{
		LinearPoll:       Duration{30 * time.Second},
		ExternalPoll:     Duration{5 * time.Second},
		Assignee:         "me",
		RepoRoots:        []string{"~/src", "~/git"},
		DefaultAgent:     "claude",
		BranchTemplate:   "{key_lower}/{slug}",
		WorktreeTemplate: "{repo}/.worktrees/{branch}",
		PromptTemplate:   DefaultPrompt,
		EndedDays:        3,
		GroupByProject:   true,
		DimInactive:      true,
		Coordinator:      CoordinatorConfig{Start: "ask", Approve: "ask"},
		Review:           ReviewConfig{Prompt: DefaultReviewPrompt, Post: "ask", FindTools: DefaultFindTools},
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
	switch c.NotifyVia {
	case "", "auto", "terminal", "system":
	default:
		return c, fmt.Errorf("%s: notify_via must be \"auto\", \"terminal\" or \"system\", got %q", path, c.NotifyVia)
	}
	for key, v := range map[string][2]string{
		"coordinator.start":   {c.Coordinator.Start, "ask auto panel"},
		"coordinator.approve": {c.Coordinator.Approve, "ask auto off"},
		"review.post":         {c.Review.Post, "ask never auto"},
	} {
		if !slices.Contains(strings.Fields(v[1]), v[0]) {
			return c, fmt.Errorf("%s: %s must be one of %s, got %q", path, key, strings.Join(strings.Fields(v[1]), ", "), v[0])
		}
	}
	if err := CheckKeys(c.Keys); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for i, r := range c.RepoRoots {
		c.RepoRoots[i] = Expand(r)
	}
	c.LaunchDir = Expand(c.LaunchDir)
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

// SocketPath is the panel's hook socket, computed without touching the disk (the hook
// client runs on every agent tool call and must stay cheap).
func SocketPath() string {
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "lanes", "lanes.sock")
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
