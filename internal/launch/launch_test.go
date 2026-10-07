package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// clone makes root/<name> cloned from a bare origin that has main + a feature branch.
func clone(t *testing.T, root, name, originURL string) string {
	bare := filepath.Join(t.TempDir(), name+".git")
	seed := t.TempDir()
	run(t, seed, "git", "init", "-q", "-b", "main")
	run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "init")
	run(t, seed, "git", "branch", "abc-2-remote")
	run(t, seed, "git", "clone", "-q", "--bare", seed, bare)
	path := filepath.Join(root, name)
	run(t, root, "git", "clone", "-q", bare, path)
	if originURL != "" { // inference only reads the URL; nothing fetches from it
		run(t, path, "git", "remote", "set-url", "origin", originURL)
	}
	return path
}

func TestInferRepo(t *testing.T) {
	root := t.TempDir()
	clone(t, root, "widget-api", "git@github.com:acme/widget-api.git")
	clone(t, root, "widget", "https://github.com/acme/widget")
	os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755)
	repos := Repos([]string{root, "/does/not/exist"})
	if len(repos) != 2 {
		t.Fatalf("repos %+v", repos)
	}
	cases := []struct {
		name string
		d    tracker.IssueDetail
		want string
	}{
		{"ssh PR remote", tracker.IssueDetail{PRURLs: []string{"https://github.com/acme/widget-api/pull/1"}}, "widget-api"},
		{"https PR remote", tracker.IssueDetail{PRURLs: []string{"https://github.com/ACME/widget/pull/9"}}, "widget"},
		{"longest mention wins", tracker.IssueDetail{Description: "change `widget-api` handlers"}, "widget-api"},
		{"short mention", tracker.IssueDetail{Description: "the widget repo"}, "widget"},
		{"no partial word", tracker.IssueDetail{Description: "widgets everywhere"}, ""},
	}
	for _, c := range cases {
		got, ok := InferRepo(c.d, repos)
		if got.Name != c.want || ok != (c.want != "") {
			t.Errorf("%s: got %q ok=%v", c.name, got.Name, ok)
		}
	}
}

func TestWorktreeCases(t *testing.T) {
	ctx := context.Background()
	repo := clone(t, t.TempDir(), "app", "")
	wt := t.TempDir()

	// new branch from origin/main
	p1 := filepath.Join(wt, "new")
	if err := Worktree(ctx, repo, "abc-1/new", p1); err != nil {
		t.Fatal(err)
	}
	if b := run(t, p1, "git", "branch", "--show-current"); b != "abc-1/new" {
		t.Fatalf("branch %q", b)
	}
	if up := exec.Command("git", "-C", p1, "rev-parse", "--abbrev-ref", "@{u}").Run(); up == nil {
		t.Fatal("new branch should not track origin/main")
	}
	// reuse: same path, same branch
	if err := Worktree(ctx, repo, "abc-1/new", p1); err != nil {
		t.Fatalf("reuse: %v", err)
	}
	// existing path on another branch is refused
	if err := Worktree(ctx, repo, "abc-9/other", p1); err == nil {
		t.Fatal("want error for path on another branch")
	}
	// remote-only branch is tracked
	p2 := filepath.Join(wt, "remote")
	if err := Worktree(ctx, repo, "abc-2-remote", p2); err != nil {
		t.Fatal(err)
	}
	if up := run(t, p2, "git", "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/abc-2-remote" {
		t.Fatalf("upstream %q", up)
	}
	// existing local branch is checked out as-is
	run(t, repo, "git", "branch", "abc-3-local", "origin/main")
	if err := Worktree(ctx, repo, "abc-3-local", filepath.Join(wt, "local")); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	live := []state.Record{{ID: "a", Tool: "claude", TicketKey: "ABC-1", Worktree: "/w/1"}}
	if _, err := Check(live, "/w/1", "ABC-9"); err == nil {
		t.Fatal("same worktree must be refused")
	}
	if w, err := Check(live, "/w/2", "ABC-1"); err != nil || !strings.Contains(w, "ABC-1") {
		t.Fatalf("w=%q err=%v", w, err)
	}
	if w, err := Check(live, "/w/2", "ABC-2"); err != nil || w != "" {
		t.Fatalf("w=%q err=%v", w, err)
	}
}

func TestSlugAndPlan(t *testing.T) {
	if s := Slug("Fix: the LOGIN page (again!) and a very long tail of words here"); s != "fix-the-login-page-again-and-a-very-long" {
		t.Fatalf("slug %q", s)
	}
	cfg := config.Default()
	cfg.Preamble = "Work on {key}."
	cfg.Agents = map[string]config.AgentConfig{"fake": {Args: []string{"--x"}}}
	d := tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-12", Title: "Fix login", URL: "https://t.example/ABC-12"}, Description: "Body"}
	p := NewPlan(d, Repo{"app", "/src/app"}, fake{}, cfg)
	if p.Branch != "abc-12/fix-login" || p.Worktree != "/src/app/.worktrees/abc-12/fix-login" || p.Args[0] != "--x" {
		t.Fatalf("plan %+v", p)
	}
	if want := "Work on ABC-12.\n\nTicket ABC-12: Fix login\nhttps://t.example/ABC-12\n\nBody"; p.Prompt != want {
		t.Fatalf("prompt %q", p.Prompt)
	}
}

type fake struct{}

func (fake) Name() string                                { return "fake" }
func (fake) List(context.Context) ([]agent.Agent, error) { return nil, nil }
func (fake) Stop() (string, []string)                    { return "", nil }
func (fake) Command(s agent.LaunchSpec) agent.Command {
	return agent.Command{Argv: append([]string{"fake", s.Name}, s.Prompt), SessionID: "sid"}
}

type fakeTmux struct {
	argv, env []string
	opts      map[string]string
	optErr    error
	killed    []string
}

func (f *fakeTmux) NewSession(name, cwd string, env, argv []string) (string, error) {
	f.argv, f.env = argv, env
	return "%7", nil
}
func (f *fakeTmux) SetOpt(pane, key, val string) error {
	f.opts = map[string]string{pane + key: val}
	return f.optErr
}
func (f *fakeTmux) KillSession(name string) error {
	f.killed = append(f.killed, name)
	return nil
}

func TestRun(t *testing.T) {
	repo := clone(t, t.TempDir(), "app", "")
	store := state.Store{Dir: t.TempDir()}
	tm := &fakeTmux{}
	p := NewPlan(tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-5", Title: "Do thing"}}, Repo{"app", repo}, fake{}, config.Default())
	rec, err := Run(context.Background(), p, tm, store)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Pane != "%7" || rec.SessionID != "sid" || rec.TicketKey != "ABC-5" || tm.opts["%7@lanes_agent"] != rec.ID {
		t.Fatalf("rec %+v opts %v", rec, tm.opts)
	}
	if tm.argv[1] != "ABC-5 do-thing" || tm.env[len(tm.env)-1] != "LANES_AGENT_ID="+rec.ID {
		t.Fatalf("argv %q env %q", tm.argv, tm.env)
	}
	if all, _ := store.All(); len(all) != 1 || all[0].ID != rec.ID {
		t.Fatalf("saved %+v", all)
	}
}

func TestRunKillsUntrackableSession(t *testing.T) {
	repo := clone(t, t.TempDir(), "app", "")
	store := state.Store{Dir: t.TempDir()}
	tm := &fakeTmux{optErr: errors.New("no tmux")}
	p := NewPlan(tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-6", Title: "x"}}, Repo{"app", repo}, fake{}, config.Default())
	rec, err := Run(context.Background(), p, tm, store)
	if err == nil || len(tm.killed) != 1 || tm.killed[0] != rec.Session() {
		t.Fatalf("err=%v killed=%v", err, tm.killed)
	}
	if all, _ := store.All(); len(all) != 0 {
		t.Fatalf("record saved for a killed session: %+v", all)
	}
}

func TestAdoptRunsInPlace(t *testing.T) {
	repo := clone(t, t.TempDir(), "app", "")
	sub := filepath.Join(repo, "sub")
	os.MkdirAll(sub, 0o755)
	p := AdoptPlan(context.Background(), tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-7", Title: "t"}}, sub, "orig", fake{}, config.Default())
	if !p.InPlace || p.Worktree != sub || p.Branch != "main" || p.ResumeFrom != "orig" || p.Repo.Name != "app" {
		t.Fatalf("plan %+v", p)
	}
	tm := &fakeTmux{}
	rec, err := Run(context.Background(), p, tm, state.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Worktree != sub {
		t.Fatalf("rec %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees")); err == nil {
		t.Fatal("adopt created a worktree")
	}
}

func TestFolderPlanUsesTheFolderAsIs(t *testing.T) {
	dir := t.TempDir() // not a repo
	cfg := config.Default()
	cfg.PromptTemplate = "{key} in {repo}"
	p := FolderPlan(context.Background(), tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-3", Title: "t"}}, dir, fake{}, cfg)
	if !p.InPlace || p.Worktree != dir || p.Branch != "" || p.Repo.Path != dir || p.Prompt != "ABC-3 in "+dir {
		t.Fatalf("plan %+v", p)
	}
	repo := clone(t, t.TempDir(), "app", "")
	p = FolderPlan(context.Background(), tracker.IssueDetail{Issue: tracker.Issue{Key: "ABC-3"}}, repo, fake{}, cfg)
	if p.Branch != "main" || p.Repo.Name != "app" {
		t.Fatalf("plan in a checkout %+v", p)
	}
}
