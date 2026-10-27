package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/launch"
)

// prRepo makes a clone "widget" under a repo root whose origin has pull request 12.
func prRepo(t *testing.T) (root, repo string) {
	root = t.TempDir()
	repo = filepath.Join(root, "widget")
	bare := filepath.Join(t.TempDir(), "acme", "widget.git") // origin is acme/widget
	for _, c := range [][]string{
		{"git", "init", "-q", "--bare", "-b", "main", bare},
		{"git", "clone", "-q", bare, repo},
		{"git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "init"},
		{"git", "-C", repo, "push", "-q", "origin", "main"},
		{"git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "the change"},
		{"git", "-C", repo, "push", "-q", "origin", "HEAD:refs/pull/12/head"},
		{"git", "-C", repo, "reset", "-q", "--hard", "HEAD~1"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, out)
		}
	}
	return root, repo
}

func fakeGH(_ context.Context, _ string, args ...string) ([]byte, error) {
	return []byte(`{"number":12,"title":"Fix the login","url":"https://github.com/acme/widget/pull/12"}`), nil
}

func TestReviewAPullRequest(t *testing.T) {
	root, repo := prRepo(t)
	m, ft := controlModel(t)
	m.opt.Config.RepoRoots = []string{root}
	m.readers.GH = fakeGH
	m.Update(key("R"))
	if m.modal == nil || !m.modal.input {
		t.Fatalf("%+v", m.modal)
	}
	m.modal.text = "acme/widget#12"
	_, cmd := m.Update(key("enter"))
	_, cmd = m.Update(cmd()) // reviewFoundMsg → the start screen
	text := strings.Join(m.modal.lines, "\n")
	if m.modal == nil || m.modal.title != "Review acme/widget#12?" || !strings.Contains(text, "Fix the login") || !strings.Contains(text, "posts only after you say so") {
		t.Fatalf("%q %q", m.modal.title, text)
	}
	_, cmd = m.Update(key("y"))
	m.Update(cmd()) // worktree, then launchedMsg
	path := filepath.Join(repo, ".worktrees", "review", "pr-12")
	if out, err := exec.Command("git", "-C", path, "log", "-1", "--format=%s").Output(); err != nil || strings.TrimSpace(string(out)) != "the change" {
		t.Fatalf("worktree at the PR head: %q %v", out, err)
	}
	argv := strings.Join(ft.argv, "\n")
	if !strings.Contains(argv, "https://github.com/acme/widget/pull/12") || !strings.Contains(argv, "only after the user says to") {
		t.Fatalf("%s", argv)
	}
	rec := m.live[len(m.live)-1]
	if rec.Review != "https://github.com/acme/widget/pull/12" || !strings.Contains(m.notice, "reviewing widget#12") {
		t.Fatalf("%+v %q", rec, m.notice)
	}
	m.Update(agentsMsg{agents: append(m.agents, agent.Agent{ID: "r", Tool: "claude", Name: "review", RecordID: rec.ID, Review: rec.Review, Status: agent.Idle}), live: m.live})
	if v := view(m); !strings.Contains(v, "REVIEWS") || !rowHas(v, "○ widget#12", "idle") {
		t.Fatalf("%s", v)
	}
	// the same PR again: shows the agent that's on it
	m.Update(key("R"))
	m.modal.text = "acme/widget#12"
	_, cmd = m.Update(key("enter"))
	m.Update(cmd())
	if m.modal != nil || !strings.Contains(m.notice, "being reviewed already") {
		t.Fatalf("%+v %q", m.modal, m.notice)
	}
}

func TestReviewPostRules(t *testing.T) {
	root, _ := prRepo(t)
	for post, want := range map[string]string{
		"never": "--disallowedTools=Bash(gh pr review:*),Bash(gh pr comment:*),Bash(gh pr edit:*),Bash(gh pr merge:*),Bash(gh pr close:*),Bash(gh issue comment:*),Bash(gh api:*)",
		"auto":  "--allowedTools=Bash(gh pr review 12 --repo acme/widget --comment:*)", // a comment review, not --approve
	} {
		m, ft := controlModel(t)
		m.opt.Adapters = []agent.Adapter{claude.New()}
		m.opt.Config.RepoRoots, m.opt.Config.Review.Post = []string{root}, post
		m.readers.GH = fakeGH
		m.Update(key("R"))
		m.modal.text = "https://github.com/acme/widget/pull/12"
		_, cmd := m.Update(key("enter"))
		m.Update(cmd())
		_, cmd = m.Update(key("y"))
		m.Update(cmd())
		if argv := strings.Join(ft.argv, "\n"); !strings.Contains(argv, want) || !strings.Contains(argv, "--setting-sources=user") {
			t.Fatalf("%s: %q", post, ft.argv)
		}
	}
}

func TestReviewNeedsTheRightClone(t *testing.T) {
	root, _ := prRepo(t)
	m, _ := controlModel(t)
	m.opt.Config.RepoRoots = []string{root}
	m.readers.GH = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"number":12,"title":"x","url":"https://github.com/otherorg/widget/pull/12"}`), nil
	}
	m.Update(key("R"))
	m.modal.text = "otherorg/widget#12"
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.modal != nil || !strings.Contains(m.notice, "is another repository's") {
		t.Fatalf("%+v %q", m.modal, m.notice)
	}
}

func TestReviewLeavesTheMainCheckoutAlone(t *testing.T) {
	_, repo := prRepo(t)
	path := filepath.Join(repo, ".worktrees", "review", "pr-12")
	os.MkdirAll(path, 0o700) // a leftover folder, not a worktree
	err := launch.ReviewWorktree(context.Background(), repo, 12, path)
	if err == nil || !strings.Contains(err.Error(), "isn't a git worktree") {
		t.Fatalf("%v", err)
	}
	if out, _ := exec.Command("git", "-C", repo, "log", "-1", "--format=%s").Output(); strings.TrimSpace(string(out)) != "init" {
		t.Fatalf("the main checkout moved: %q", out)
	}
}

func TestPasteIntoTextBoxes(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(key("R"))
	m.Update(tea.PasteMsg{Content: "https://github.com/acme/widget/pull/12\n"})
	if m.modal == nil || m.modal.text != "https://github.com/acme/widget/pull/12" {
		t.Fatalf("%+v", m.modal)
	}
	m.modal = nil
	m.Update(key("/"))
	m.Update(tea.PasteMsg{Content: "fix it"})
	if m.filter != "fix it" {
		t.Fatalf("filter %q", m.filter)
	}
}

// finder is an adapter that finds PRs (and records what it was allowed to use).
type finder struct {
	stopper
	answer string
	tools  *[]string
	asked  *string
}

func (f finder) Name() string { return "finderbot" }
func (f finder) Find(_ context.Context, prompt string, tools []string) (string, error) {
	*f.tools, *f.asked = tools, prompt
	return f.answer, nil
}

func TestReviewFromADescription(t *testing.T) {
	root, _ := prRepo(t)
	var tools []string
	var asked string
	m, _ := controlModel(t)
	m.opt.Config.RepoRoots = []string{root}
	m.readers.GH = fakeGH
	m.opt.Adapters = []agent.Adapter{finder{answer: `Here you go: {"prs":[{"url":"https://github.com/acme/widget/pull/12","why":"sam posted it in #code-reviews"},{"url":"https://github.com/acme/widget/pull/9","why":"older"},{"url":"a/b#--web","why":"steered"},{"url":"https://example.slack.com/x","why":"not a PR"}],"note":""}`, tools: &tools, asked: &asked}}
	m.Update(key("R"))
	m.modal.text = "latest PR from sam in #code-reviews"
	_, cmd := m.Update(key("enter"))
	_, cmd = m.Update(cmd()) // foundPRsMsg: two → pick one
	if !strings.Contains(asked, "latest PR from sam in #code-reviews") || !slices.Contains(tools, "Bash(gh pr view:*)") {
		t.Fatalf("asked %q with %q", asked, tools)
	}
	if m.modal == nil || len(m.modal.items) != 2 || !strings.Contains(m.modal.items[0].label, "widget#12") {
		t.Fatalf("%+v", m.modal)
	}
	_, cmd = m.Update(key("enter"))
	m.Update(cmd()) // looked up: the start screen
	if m.modal == nil || m.modal.title != "Review acme/widget#12?" {
		t.Fatalf("%+v", m.modal)
	}
}

func TestReviewFindsNothing(t *testing.T) {
	var tools []string
	var asked string
	m, _ := controlModel(t)
	m.opt.Adapters = []agent.Adapter{finder{answer: `{"prs":[],"note":"no PR from sam this week"}`, tools: &tools, asked: &asked}}
	m.Update(key("R"))
	m.modal.text = "sam's PR"
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.modal != nil || !strings.Contains(m.notice, "no PR from sam this week") {
		t.Fatalf("%+v %q", m.modal, m.notice)
	}
}
