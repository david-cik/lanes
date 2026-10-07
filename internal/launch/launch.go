// Package launch turns "start an agent on this ticket" into a worktree, a tmux
// session running the agent, and a saved record.
package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/gitinfo"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
	"github.com/david-cik/lanes/internal/tracker"
)

type Repo struct{ Name, Path string }

// Repos lists git repositories directly under each root (missing roots are skipped).
func Repos(roots []string) []Repo {
	var out []Repo
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil && e.IsDir() {
				out = append(out, Repo{e.Name(), p})
			}
		}
	}
	return out
}

var remoteRe = regexp.MustCompile(`[:/]([^/:]+/[^/]+?)(?:\.git)?/?$`)

// remote returns "owner/repo" for a repo's origin, or "".
func remote(path string) string {
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

var prRe = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/`)

// InferRepo picks the repo a ticket most likely belongs to: the origin of a linked PR,
// else the longest local repo name mentioned in the description.
func InferRepo(d tracker.IssueDetail, repos []Repo) (Repo, bool) {
	for _, u := range d.PRURLs {
		m := prRe.FindStringSubmatch(u)
		if m == nil {
			continue
		}
		for _, r := range repos {
			if remote(r.Path) == strings.ToLower(m[1]) {
				return r, true
			}
		}
	}
	byLen := slices.Clone(repos)
	slices.SortFunc(byLen, func(a, b Repo) int { return len(b.Name) - len(a.Name) })
	for _, r := range byLen {
		re := regexp.MustCompile(`(?i)(^|[^\w-])` + regexp.QuoteMeta(r.Name) + `($|[^\w-])`)
		if re.MatchString(d.Description) {
			return r, true
		}
	}
	return Repo{}, false
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a short branch-safe slug from a title.
func Slug(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// Render fills {placeholders} in a template.
func Render(tmpl string, vars map[string]string) string {
	var pairs []string
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.TrimSpace(strings.NewReplacer(pairs...).Replace(tmpl))
}

var git = gitinfo.Git

// Worktree makes sure path is a worktree of repo on branch, creating it from
// origin's default branch when the branch is new. It never touches local branches
// other than the new one.
func Worktree(ctx context.Context, repo, branch, path string) error {
	if _, err := os.Stat(path); err == nil {
		got, err := git(ctx, path, "branch", "--show-current")
		if err != nil {
			return fmt.Errorf("%s exists but is not a git worktree: %w", path, err)
		}
		if got != branch {
			return fmt.Errorf("%s is on branch %q, expected %q", path, got, branch)
		}
		return nil
	}
	if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err := git(ctx, repo, "worktree", "add", path, branch) // works offline
		return err
	}
	if _, err := git(ctx, repo, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		_, err := git(ctx, repo, "worktree", "add", "--track", "-b", branch, path, "origin/"+branch)
		return err
	}
	base, err := gitinfo.DefaultBranch(ctx, repo)
	if err != nil {
		return err
	}
	_, err = git(ctx, repo, "worktree", "add", "--no-track", "-b", branch, path, "origin/"+base)
	return err
}

// Check returns an error if another live agent already uses worktree, and a warning
// if one is already working the same ticket.
func Check(live []state.Record, worktree, ticket string) (warning string, err error) {
	for _, r := range live {
		if r.Worktree == worktree {
			return "", fmt.Errorf("agent %s (%s) is already running in %s", r.ID, r.Tool, worktree)
		}
	}
	for _, r := range live {
		if r.TicketKey == ticket {
			return fmt.Sprintf("%s already has a running %s agent (%s)", ticket, r.Tool, r.ID), nil
		}
	}
	return "", nil
}

// Tmux is the part of tmux.Client launching needs.
type Tmux interface {
	NewSession(name, cwd string, env, argv []string) (string, error)
	SetOpt(pane, key, val string) error
	KillSession(name string) error
}

// Plan is a resolved launch, shown to the user before it runs.
type Plan struct {
	Ticket   tracker.IssueDetail
	Repo     Repo
	Branch   string
	Worktree string
	Prompt   string
	Adapter  agent.Adapter
	Args     []string
	Warning  string
	HookBin  string // lanes executable the agent's hooks call; "" = no hooks
	Socket   string // panel socket the hooks report to

	InPlace    bool   // run in Worktree as it is (adopting a session): no worktree or branch is created
	ResumeFrom string // tool session to fork (adopt)
}

// FolderPlan starts a new agent for ticket d in dir as it is: no worktree or branch is
// created. Repo and branch are whatever dir already is (if it's a git checkout at all).
func FolderPlan(ctx context.Context, d tracker.IssueDetail, dir string, a agent.Adapter, cfg config.Config) Plan {
	repo := dir
	if top, err := git(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		repo = top
	}
	branch, _ := git(ctx, dir, "branch", "--show-current")
	p := NewPlan(d, Repo{filepath.Base(repo), repo}, a, cfg)
	p.Worktree, p.Branch, p.InPlace = dir, branch, true
	return p
}

// AdoptPlan forks an existing session in its own directory, for ticket d.
func AdoptPlan(ctx context.Context, d tracker.IssueDetail, dir, sessionID string, a agent.Adapter, cfg config.Config) Plan {
	repo := dir
	if top, err := git(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		repo = top
	}
	branch, _ := git(ctx, dir, "branch", "--show-current")
	return Plan{
		Ticket: d, Repo: Repo{filepath.Base(repo), repo}, Adapter: a, Args: cfg.Agents[a.Name()].Args,
		Branch: branch, Worktree: dir, InPlace: true, ResumeFrom: sessionID,
	}
}

// Run creates the worktree, starts the agent in its own tmux session, and saves the record.
func Run(ctx context.Context, p Plan, tm Tmux, store state.Store) (state.Record, error) {
	if !p.InPlace {
		if err := Worktree(ctx, p.Repo.Path, p.Branch, p.Worktree); err != nil {
			return state.Record{}, err
		}
	}
	rec := state.Record{
		ID: state.NewID(), Tool: p.Adapter.Name(), TicketKey: p.Ticket.Key, Repo: p.Repo.Path,
		Worktree: p.Worktree, Branch: p.Branch, CreatedAt: time.Now().UTC(),
	}
	rec.Name = strings.TrimSpace(p.Ticket.Key + " " + Slug(p.Ticket.Title))
	cmd := p.Adapter.Command(agent.LaunchSpec{
		Name: rec.Name, Prompt: p.Prompt, Args: p.Args, HookBin: p.HookBin,
		ResumeFrom: p.ResumeFrom,
	})
	rec.SessionID = cmd.SessionID
	env := append(cmd.Env, "LANES_AGENT_ID="+rec.ID)
	if p.Socket != "" {
		env = append(env, "LANES_SOCKET="+p.Socket)
	}
	pane, err := tm.NewSession(rec.Session(), p.Worktree, env, cmd.Argv)
	if err != nil {
		return rec, err
	}
	rec.Pane = pane
	// Without the pane tag or the record lanes could never find this agent again,
	// so don't leave it running untracked.
	if err := tm.SetOpt(pane, tmux.OptAgent, rec.ID); err != nil {
		tm.KillSession(rec.Session())
		return rec, err
	}
	if err := store.Save(rec); err != nil {
		tm.KillSession(rec.Session())
		return rec, err
	}
	return rec, nil
}

// NewPlan renders branch, worktree, and prompt from the config templates.
func NewPlan(d tracker.IssueDetail, repo Repo, a agent.Adapter, cfg config.Config) Plan {
	vars := map[string]string{
		"key": d.Key, "key_lower": strings.ToLower(d.Key), "slug": Slug(d.Title),
		"title": d.Title, "url": d.URL, "description": d.Description, "repo": repo.Path,
	}
	vars["preamble"] = Render(cfg.Preamble, vars)
	vars["branch"] = Render(cfg.BranchTemplate, vars)
	return Plan{
		Ticket: d, Repo: repo, Adapter: a, Args: cfg.Agents[a.Name()].Args,
		Branch:   vars["branch"],
		Worktree: Render(cfg.WorktreeTemplate, vars),
		Prompt:   Render(cfg.PromptTemplate, vars),
	}
}
