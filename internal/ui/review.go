package ui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/gh"
	"github.com/david-cik/lanes/internal/launch"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

// A review agent reviews a pull request you paste (R): it runs in a worktree at the PR's
// head, told by review.prompt what to look for and by review.post what it may post.

type reviewFoundMsg struct {
	pr   gh.Ref
	repo launch.Repo
	err  error
}

// postRules are shell commands that change a pull request or post to GitHub, which
// review.post "never" forbids for tools that take such rules. Best effort: they're the
// usual ways, not every way (curl could still post).
var postRules = []string{"gh pr review", "gh pr comment", "gh pr edit", "gh pr merge", "gh pr close", "gh issue comment", "gh api"}

// reviewPR asks which pull request to review.
func (m *Model) reviewPR() tea.Cmd {
	if m.opt.Tmux == nil {
		m.say("", fmt.Errorf("run lanes inside tmux to launch agents"))
		return nil
	}
	run, roots := m.readers.GH, m.opt.Config.RepoRoots
	if run == nil {
		run = gh.Exec
	}
	m.modal = &modal{input: true, title: "Review which pull request? (its URL, or owner/repo#N)",
		onInput: func(text string) tea.Cmd {
			if strings.TrimSpace(text) == "" {
				return nil
			}
			m.say("looking up "+strings.TrimSpace(text)+"…", nil)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				pr, err := gh.Lookup(ctx, run, text)
				if err != nil {
					return reviewFoundMsg{err: err}
				}
				repo, err := launch.FindRepo(ctx, launch.Repos(roots), pr.Repo)
				return reviewFoundMsg{pr: pr, repo: repo, err: err}
			}
		}}
	return nil
}

// reviewOf names the pull request an agent reviews, "name#N", from its URL.
func reviewOf(url string) string {
	parts := strings.Split(strings.TrimPrefix(url, "https://"), "/")
	if len(parts) < 5 {
		return url
	}
	return parts[2] + "#" + parts[4]
}

// confirmReview shows what a review agent will do, and starts it on yes. A pull request
// with a review agent running already shows that one.
func (m *Model) confirmReview(msg reviewFoundMsg, a agent.Adapter) tea.Cmd {
	if msg.err != nil {
		m.say("", msg.err)
		return nil
	}
	pr := msg.pr
	if i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.Review == pr.URL }); i >= 0 {
		m.focus(m.live[i].ID, m.live[i].Pane)
		m.say(fmt.Sprintf("%s#%d is being reviewed already", pr.Repo, pr.Number), nil)
		return nil
	}
	cfg := m.opt.Config
	if a == nil {
		a = m.adapter(cmp.Or(cfg.Review.Agent, cfg.DefaultAgent))
	}
	if a == nil && len(m.opt.Adapters) > 0 {
		a = m.opt.Adapters[0]
	}
	if a == nil {
		m.say("", fmt.Errorf("no agent tool is installed"))
		return nil
	}
	post := cmp.Or(cfg.Review.Post, "ask")
	vars := map[string]string{"url": pr.URL, "repo": pr.Repo, "number": strconv.Itoa(pr.Number), "title": pr.Title}
	vars["post"] = launch.Render(config.ReviewPost[post], vars)
	args := slices.Clone(cfg.Agents[a.Name()].Args)
	enforced := false
	if r, ok := a.(agent.Ruler); ok {
		switch post {
		case "never":
			args, enforced = append(args, r.RuleArgs(nil, postRules)...), true
		case "auto": // a comment review of this PR, nothing else (not --approve)
			args, enforced = append(args, r.RuleArgs([]string{fmt.Sprintf("gh pr review %d --repo %s --comment", pr.Number, pr.Repo)}, nil)...), true
		}
	}
	// The PR is someone else's code: its folder may bring settings that run commands.
	isolation := "⚠ " + a.Name() + " loads the settings the PR's code brings (hooks, tools) if you trust the folder"
	if iso, ok := a.(agent.Isolator); ok {
		args, isolation = append(args, iso.IsolateArgs()...), "settings the PR's code brings (hooks, tools) are ignored"
	}
	path := launch.ReviewPath(msg.repo.Path, pr.Number)
	p := launch.Plan{
		Ticket: tracker.IssueDetail{Issue: tracker.Issue{Key: state.NoTicket}}, Repo: msg.repo, Adapter: a,
		Worktree: path, InPlace: true, Name: fmt.Sprintf("review · %s#%d", pr.Repo, pr.Number), Review: pr.URL,
		Args: args, Prompt: launch.Render(cmp.Or(cfg.Review.Prompt, config.DefaultReviewPrompt), vars),
		Socket: m.opt.Socket,
	}
	if _, ok := a.(agent.Hooker); ok {
		p.HookBin = m.opt.HookBin
	}
	postLine := map[string]string{"ask": "posts only after you say so (it asks)", "never": "never posts: it drafts, you post",
		"auto": "posts its review when done"}[post]
	if !enforced && post != "ask" {
		postLine += " (told so; " + a.Name() + " takes no rules to enforce it)"
	}
	tm, store := m.opt.Tmux, m.opt.Store
	start := func() (tea.Cmd, bool) {
		m.say(fmt.Sprintf("checking out %s#%d…", pr.Repo, pr.Number), nil)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := launch.ReviewWorktree(ctx, msg.repo.Path, pr.Number, path); err != nil {
				return launchedMsg{err: err}
			}
			rec, err := launch.Run(ctx, p, tm, store)
			return launchedMsg{rec: rec, err: err}
		}, false
	}
	acts := []action{{"y", "start", start}, {"enter", "start", start}}
	if len(m.opt.Adapters) > 1 {
		acts = append(acts, action{"t", "use another tool (now " + a.Name() + ")", func() (tea.Cmd, bool) {
			var items []choice
			for _, o := range m.opt.Adapters {
				if o.Name() != a.Name() {
					items = append(items, choice{o.Name(), o.Name()})
				}
			}
			m.modal = &modal{title: "Review with which tool?", items: items,
				onChoose: func(c choice) tea.Cmd { return m.confirmReview(msg, m.adapter(c.value)) }}
			return nil, true
		}})
	}
	m.notice = ""
	m.modal = &modal{title: fmt.Sprintf("Review %s#%d?", pr.Repo, pr.Number), lines: []string{
		pr.Title,
		"",
		"tool:     " + a.Name(),
		"worktree: " + home(path) + " (the PR's head)",
		"posting:  " + postLine,
		"its code: " + isolation,
		faint.Render("(review.prompt / review.post in the config)"),
	}, actions: acts}
	return nil
}
