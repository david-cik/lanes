package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

type fakeTmux struct {
	log      []string
	sessions map[string]bool
	gone     map[string]bool // panes that no longer exist
	screen   string
}

func (f *fakeTmux) rec(s string) error { f.log = append(f.log, s); return nil }
func (f *fakeTmux) NewSession(name, cwd string, env, argv []string) (string, error) {
	f.sessions[name] = true
	return "%9", f.rec("new " + name)
}
func (f *fakeTmux) SetOpt(p, k, v string) error { return f.rec("opt " + p + " " + k + "=" + v) }
func (f *fakeTmux) Swap(a, b string) error {
	if f.gone[a] || f.gone[b] {
		f.rec("swap-failed " + a + " " + b)
		return errors.New("can't find pane")
	}
	return f.rec("swap " + a + " " + b)
}
func (f *fakeTmux) Join(s, d string, _ int) error { return f.rec("join " + s + " " + d) }
func (f *fakeTmux) Select(p string) error         { return f.rec("select " + p) }
func (f *fakeTmux) SendText(p, t string) error    { return f.rec("text " + p + " " + t) }
func (f *fakeTmux) SendKeys(p string, k ...string) error {
	return f.rec("keys " + p + " " + strings.Join(k, ","))
}
func (f *fakeTmux) KillSession(n string) error       { delete(f.sessions, n); return f.rec("kill " + n) }
func (f *fakeTmux) Capture(p string) (string, error) { return f.screen, nil }
func (f *fakeTmux) HasSession(n string) bool         { return f.sessions[n] }

type stopper struct{ agent.Adapter }

func (stopper) Name() string                                { return "claude" }
func (stopper) List(context.Context) ([]agent.Agent, error) { return nil, nil }
func (stopper) Stop() (string, []string)                    { return "/exit", nil }
func (stopper) Command(s agent.LaunchSpec) agent.Command {
	return agent.Command{Argv: []string{"claude", s.Prompt}, SessionID: "sid"}
}

func controlModel(t *testing.T) (*Model, *fakeTmux) {
	ft := &fakeTmux{sessions: map[string]bool{"lanes-r1": true, "lanes-r2": true}, gone: map[string]bool{}}
	m := New(Options{
		Tracker: &fakeTracker{}, Assignee: "me", Tmux: ft, Panel: "%0", Placeholder: "%P",
		Adapters: []agent.Adapter{stopper{}}, Store: state.Store{Dir: t.TempDir()},
		Config: config.Default(), LinearPoll: time.Minute, ExternalPoll: time.Second,
		Issues: []tracker.Issue{{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted"}},
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	snap := &fleetSnap{
		agents: []agent.Agent{
			{ID: "s1", Tool: "claude", Name: "one", TicketKey: "ABC-1", RecordID: "r1", Pane: "%1"},
			{ID: "s2", Tool: "claude", Name: "two", TicketKey: "ABC-1", RecordID: "r2", Pane: "%2"},
			{ID: "x9", Tool: "claude", Name: "outside", Branch: "main", External: true},
		},
		live: []state.Record{
			{ID: "r1", Tool: "claude", TicketKey: "ABC-1", Pane: "%1"},
			{ID: "r2", Tool: "claude", TicketKey: "ABC-1", Pane: "%2"},
		},
	}
	for _, r := range snap.live { // launch records exist on disk, as in a real run
		if err := m.opt.Store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	m.opt.Fleet = snap
	m.Update(agentsMsg{agents: snap.agents, live: snap.live})
	return m, ft
}

// fleetSnap answers agent refreshes with a fixed snapshot tests can edit.
type fleetSnap struct {
	agents []agent.Agent
	live   []state.Record
}

func (f *fleetSnap) Snapshot(context.Context) ([]agent.Agent, []state.Record, error) {
	return append([]agent.Agent{}, f.agents...), append([]state.Record{}, f.live...), nil
}

func (m *Model) cursorTo(t *testing.T, text string) {
	t.Helper()
	stats := m.stats()
	for i := range m.rows {
		if strings.Contains(plainLine(1000, m.rowSegs(i, stats[i])...), text) {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no row containing %q", text)
}

func TestFocusSwapsCurrentAgentHomeFirst(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("enter"))
	m.cursorTo(t, "two")
	m.Update(key("enter"))
	m.Update(key("q"))
	want := []string{"swap %1 %P", "select %1", "swap %1 %P", "swap %2 %P", "select %2", "swap %2 %P"}
	if strings.Join(ft.log, "|") != strings.Join(want, "|") {
		t.Fatalf("tmux calls:\n%s\nwant:\n%s", strings.Join(ft.log, "\n"), strings.Join(want, "\n"))
	}
}

func TestExternalAgentCannotBeFocusedButCanBeLinked(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "outside")
	m.Update(key("enter"))
	if len(ft.log) != 0 || !strings.Contains(m.notice, "not started by lanes") {
		t.Fatalf("log=%v notice=%q", ft.log, m.notice)
	}
	m.Update(key("t"))
	m.Update(key("enter")) // first ticket: ABC-1
	links, _ := m.opt.Store.Links()
	if links["claude:x9"] != "ABC-1" {
		t.Fatalf("links %v", links)
	}
	if !strings.Contains(view(m), "outside") || strings.Contains(view(m), "Unlinked") {
		t.Fatalf("agent not moved under ticket:\n%s", view(m))
	}
	m.cursorTo(t, "outside")
	m.Update(key("t"))
	if m.modal.items[0].label != "Remove from ABC-1" {
		t.Fatalf("first choice %q", m.modal.items[0].label)
	}
	m.Update(key("enter"))
	if links, _ := m.opt.Store.Links(); links["claude:x9"] != state.NoTicket {
		t.Fatalf("removal not remembered: %v", links)
	}
	if v := view(m); strings.Index(v, "UNLINKED") < 0 || strings.Index(v, "outside") < strings.Index(v, "UNLINKED") {
		t.Fatalf("session not under Unlinked:\n%s", view(m))
	}
}

func TestSendAndStop(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("enter")) // show it, so stop must swap it home first
	m.Update(key("s"))
	for _, k := range []string{"h", "i"} {
		m.Update(key(k))
	}
	_, cmd := m.Update(key("enter"))
	cmd()
	m.Update(key("x"))
	_, cmd = m.Update(key("y"))
	ft.sessions["lanes-r1"] = false // the tool exited after /exit
	if msg := cmd().(noticeMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	got := strings.Join(ft.log, "|")
	if !strings.Contains(got, "text %1 hi|swap %1 %P|text %1 /exit") {
		t.Fatalf("tmux calls: %s", got)
	}
	if m.shown != "" {
		t.Fatal("stopped agent still marked shown")
	}
}

func TestShownAgentExitRestoresPlaceholder(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("enter"))
	m.Update(agentsMsg{live: []state.Record{{ID: "r2", Pane: "%2"}}})
	if m.shown != "" || ft.log[len(ft.log)-1] != "join %P %0" {
		t.Fatalf("shown=%q log=%v", m.shown, ft.log)
	}
}

func TestLaunchFlow(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "widget")
	bare := filepath.Join(t.TempDir(), "o.git")
	for _, c := range [][]string{
		{"git", "init", "-q", "--bare", "-b", "main", bare},
		{"git", "clone", "-q", bare, repo},
		{"git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "init"},
		{"git", "-C", repo, "push", "-q", "origin", "main"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, out)
		}
	}
	m, ft := controlModel(t)
	m.opt.Config.RepoRoots = []string{root}
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.cursorTo(t, "ABC-1 fix it")
	_, cmd := m.Update(key("n"))
	_, cmd = m.Update(cmd()) // detailMsg → repo inferred from description → confirm modal
	if m.modal == nil || m.modal.actions == nil || !strings.Contains(strings.Join(m.modal.lines, "\n"), "abc-1/fix-it") {
		t.Fatalf("modal %+v", m.modal)
	}
	if !strings.Contains(strings.Join(m.modal.lines, "\n"), "already has a running") {
		t.Fatalf("missing same-ticket warning: %v", m.modal.lines)
	}
	_, cmd = m.Update(key("y"))
	_, cmd = m.Update(cmd()) // launchedMsg
	if !strings.Contains(m.notice, "started claude on ABC-1") {
		t.Fatalf("notice %q", m.notice)
	}
	if !strings.Contains(strings.Join(ft.log, "|"), "swap %9 %P|select %9") {
		t.Fatalf("new agent not shown: %v", ft.log)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees", "abc-1", "fix-it")); err != nil {
		t.Fatal(err)
	}
}

type detailTracker struct{ *fakeTracker }

func (detailTracker) Issue(_ context.Context, key string) (tracker.IssueDetail, error) {
	return tracker.IssueDetail{Issue: tracker.Issue{Key: key, Title: "fix it"}, Description: "lives in the widget repo"}, nil
}

func TestFocusAfterShownAgentExitedBeforeRefresh(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("enter"))
	ft.gone["%1"] = true // agent one exits while on screen; no refresh yet
	m.cursorTo(t, "two")
	m.Update(key("enter"))
	want := "swap %1 %P|select %1|swap-failed %1 %P|join %P %0|swap %2 %P|select %2"
	if got := strings.Join(ft.log, "|"); got != want {
		t.Fatalf("tmux calls:\n%s\nwant:\n%s", got, want)
	}
	if m.shown != "r2" {
		t.Fatalf("shown %q", m.shown)
	}
}

func TestStoppingAgentIsNotControllable(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("x"))
	m.Update(key("y")) // stop started; its command is not run yet
	n := len(ft.log)
	m.Update(key("enter"))
	if len(ft.log) != n || !strings.Contains(m.notice, "stopping") {
		t.Fatalf("log=%v notice=%q", ft.log[n:], m.notice)
	}
}

func TestAdoptForksExternalSessionInPlace(t *testing.T) {
	m, ft := controlModel(t)
	m.opt.Adapters = []agent.Adapter{claudeLike{stopper{}}}
	snap := m.opt.Fleet.(*fleetSnap)
	snap.agents[2].Cwd = t.TempDir()
	m.agents = append([]agent.Agent{}, snap.agents...)
	m.rebuild()
	m.cursorTo(t, "outside")
	m.Update(keyName("A"))
	if m.modal == nil || len(m.modal.items) == 0 { // not linked to a ticket: picker first
		t.Fatalf("expected ticket picker, got %+v", m.modal)
	}
	_, cmd := m.Update(key("enter")) // ABC-1
	m.Update(cmd())                  // adoptPlanMsg
	if m.modal == nil || !m.modal.confirm || !strings.Contains(strings.Join(m.modal.lines, "\n"), "original keeps running") {
		t.Fatalf("confirm %+v", m.modal)
	}
	_, cmd = m.Update(key("y"))
	m.Update(cmd()) // launchedMsg (its follow-up commands poll the pane; not needed here)
	if !strings.Contains(strings.Join(ft.log, "|"), "new lanes-") {
		t.Fatalf("no session started: %v", ft.log)
	}
	if len(m.live) != 3 || m.live[2].Worktree != snap.agents[2].Cwd {
		t.Fatalf("live %+v", m.live)
	}
}

func TestAdoptRefusesManagedAgent(t *testing.T) {
	m, _ := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(keyName("A"))
	if m.modal != nil || !strings.Contains(m.notice, "already runs") {
		t.Fatalf("notice %q", m.notice)
	}
}

// claudeLike is a stopper that can fork.
type claudeLike struct{ stopper }

func (claudeLike) CanFork() bool { return true }

func TestAdoptInNonRepoFolderHasNoLock(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.Adapters = []agent.Adapter{claudeLike{stopper{}}}
	dir := t.TempDir() // not a git repo
	m.live = append(m.live, state.Record{ID: "r9", Tool: "claude", TicketKey: "ABC-1", Worktree: dir, Pane: "%9"})
	snap := m.opt.Fleet.(*fleetSnap)
	snap.agents[2].Cwd, snap.agents[2].TicketKey = dir, "ABC-1"
	m.agents = append([]agent.Agent{}, snap.agents...)
	m.rebuild()
	m.cursorTo(t, "outside")
	_, cmd := m.Update(keyName("A"))
	m.Update(cmd())
	if m.modal == nil || !m.modal.confirm {
		t.Fatalf("a folder that isn't a checkout should not be locked; notice %q", m.notice)
	}
}

func TestLaunchDirStartsInFolderAndCanSwitch(t *testing.T) {
	m, ft := controlModel(t)
	launchDir := t.TempDir()
	m.opt.Config.LaunchDir = launchDir
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.cursorTo(t, "ABC-1 fix it")
	_, cmd := m.Update(key("n"))
	_, cmd = m.Update(cmd()) // detailMsg → folder plan (off the loop)
	m.Update(cmd())          // planMsg → confirm
	lines := strings.Join(m.modal.lines, "\n")
	if !strings.Contains(lines, "folder:   "+home(launchDir)) || strings.Contains(lines, "worktree") {
		t.Fatalf("confirm lines:\n%s", lines)
	}
	// f → pick another folder → type a path
	other := t.TempDir()
	m.Update(key("f"))
	if m.modal == nil || m.modal.items[len(m.modal.items)-1].label != "Type a path…" {
		t.Fatalf("picker %+v", m.modal)
	}
	m.modal.pick = len(m.modal.visible()) - 1
	m.Update(key("enter"))
	m.modal.text = other
	_, cmd = m.Update(key("enter"))
	m.Update(cmd())
	if !strings.Contains(strings.Join(m.modal.lines, "\n"), "folder:   "+home(other)) {
		t.Fatalf("after picking a folder: %v", m.modal.lines)
	}
	_, cmd = m.Update(key("y"))
	m.Update(cmd())
	if !strings.Contains(strings.Join(ft.log, "|"), "new lanes-") || m.live[len(m.live)-1].Worktree != other {
		t.Fatalf("log %v live %+v", ft.log, m.live)
	}
	if _, err := os.Stat(filepath.Join(other, ".worktrees")); err == nil {
		t.Fatal("folder launch created a worktree")
	}
}

func TestFolderLaunchOfferWorktreeInstead(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.cursorTo(t, "ABC-1 fix it")
	_, cmd := m.Update(key("n"))
	_, cmd = m.Update(cmd())
	m.Update(cmd())
	m.Update(key("w")) // no repos under repo_roots in this test → explains
	if m.modal != nil && strings.HasPrefix(m.modal.title, "Start an agent") {
		t.Fatal("w did nothing")
	}
	if m.modal == nil && !strings.Contains(m.notice, "no git repos found") {
		t.Fatalf("notice %q", m.notice)
	}
}

func TestRemoveLaunchedAgentFromTicket(t *testing.T) {
	m, _ := controlModel(t)
	m.cursorTo(t, "one")
	m.Update(key("t"))
	m.Update(key("enter")) // Remove from ABC-1
	if m.live[0].TicketKey != state.NoTicket {
		t.Fatalf("record %+v", m.live[0])
	}
	if recs, _ := m.opt.Store.All(); recs[0].TicketKey != state.NoTicket {
		t.Fatalf("saved record %+v", recs[0])
	}
	if who := m.whoIs("r1"); who != "one" {
		t.Fatalf("removed agent is called %q in messages", who)
	}
	v := view(m)
	if i, j := strings.Index(v, "UNLINKED"), strings.Index(v, " one  "); i < 0 || j < i {
		t.Fatalf("launched agent not moved to Unlinked:\n%s", v)
	}
	// and onto another ticket
	m.issues = append(m.issues, tracker.Issue{Key: "ABC-2", Title: "two", Team: "Alpha", State: "Todo", StateType: "unstarted"})
	m.rebuild()
	m.cursorTo(t, " one  ")
	m.Update(key("t"))
	for i, c := range m.modal.items {
		if strings.HasPrefix(c.label, "ABC-2") {
			m.modal.pick = i
		}
	}
	m.Update(key("enter"))
	if m.live[0].TicketKey != "ABC-2" {
		t.Fatalf("record %+v", m.live[0])
	}
}

func TestFooterFitsAndHelpListsEverything(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.FocusLabel = "Ctrl-b ←/→"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.cursorTo(t, "one")
	if f := m.footer(); !strings.Contains(f, "x stop") || !strings.Contains(f, "Ctrl-b+←/→ panes") || !strings.Contains(f, "? keys") || len([]rune(f)) > 80 {
		t.Fatalf("agent footer %q (%d)", f, len([]rune(f)))
	}
	m.cursorTo(t, "ABC-1 fix it")
	if f := m.footer(); !strings.Contains(f, "n new") || strings.Contains(f, "x stop") || len([]rune(f)) > 80 {
		t.Fatalf("ticket footer %q", f)
	}
	m.Update(key("?"))
	if v := view(m); !strings.Contains(v, "take it off its ticket") || !rowHas(v, "Ctrl-b ←/→", "board ⇄ agent") || !rowHas(v, "Ctrl-b Ctrl-b", "avoid in an agent") {
		t.Fatalf("help:\n%s", v)
	}
	m.Update(key("?"))
	if m.modal != nil {
		t.Fatal("? didn't close the key list")
	}
}

func TestLinkMadeOutsidePanelShowsOnRefresh(t *testing.T) {
	m, _ := controlModel(t)
	if strings.Index(view(m), "outside") < strings.Index(view(m), "Unlinked") {
		t.Fatal("precondition: outside starts unlinked")
	}
	if _, err := m.opt.Store.SetLink("claude:x9", "ABC-1"); err != nil { // as `lanes link` would
		t.Fatal(err)
	}
	m.Update(m.fetchAgents()())
	if v := view(m); strings.Contains(v, "Unlinked") {
		t.Fatalf("CLI link not picked up:\n%s", v)
	}
	// and a panel link doesn't drop a CLI link made after the last refresh
	m.opt.Store.SetLink("claude:other", "ABC-9")
	m.cursorTo(t, "outside")
	m.Update(key("t"))
	m.Update(key("enter")) // Remove from ABC-1
	if l, _ := m.opt.Store.Links(); l["claude:other"] != "ABC-9" || l["claude:x9"] != state.NoTicket {
		t.Fatalf("links %v", l)
	}
}

func TestAskProposesThenLinksOnConfirm(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	endedTranscript(t, cfg, "x9", "/src", "outside work")
	m, _ := controlModel(t)
	var gotPrompt string
	m.readers.Ask = func(_ context.Context, system, prompt string) (string, error) {
		gotPrompt = prompt
		return "Sure: ```json\n{\"session\":\"x9\",\"ticket\":\"ABC-1\",\"reason\":\"it talks about fixing it\"}\n```", nil
	}
	m.Update(key(":"))
	for _, r := range "attach outside to ABC-1" {
		m.Update(key(string(r)))
	}
	_, cmd := m.Update(key("enter"))
	_, cmd = m.Update(cmd()) // askMsg → confirm
	if !strings.Contains(gotPrompt, "attach outside to ABC-1") || !strings.Contains(gotPrompt, `"open_tickets"`) {
		t.Fatalf("prompt %s", gotPrompt)
	}
	if m.modal == nil || m.modal.title != "Link outside to ABC-1?" || !strings.Contains(strings.Join(m.modal.lines, " "), "talks about fixing it") {
		t.Fatalf("confirm %+v", m.modal)
	}
	m.Update(key("y"))
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-1" {
		t.Fatalf("links %v", l)
	}
}

func TestAskQuestionAndGarbage(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(askMsg{question: "which of the two CSP sessions?"})
	if !strings.Contains(m.notice, "Claude asks: which of the two") || m.modal != nil {
		t.Fatalf("notice %q", m.notice)
	}
	if r := parseAsk("no json here"); r.err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestAskRejectsUnknownSession(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(askMsg{session: "made-up", ticket: "ABC-1", known: map[string]string{"x9": "outside"}})
	if m.modal != nil || !strings.Contains(m.notice, "isn't one of your recent sessions") {
		t.Fatalf("notice %q modal %+v", m.notice, m.modal)
	}
}

func TestAskTakeOff(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(askMsg{session: "x9", ticket: "-", reason: "not ticket work", known: map[string]string{"x9": "outside work"}})
	if m.modal == nil || m.modal.title != "Take outside off its ticket?" {
		t.Fatalf("confirm %+v", m.modal)
	}
	m.Update(key("y"))
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "-" {
		t.Fatalf("links %v", l)
	}
}
