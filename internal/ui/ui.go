// Package ui is the Bubble Tea board.
package ui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

type (
	issuesMsg struct {
		assignee string
		issues   []tracker.Issue
		err      error
	}
	agentsMsg struct {
		seq    int
		agents []agent.Agent
		live   []state.Record
		err    error
	}
	usersMsg struct {
		users []tracker.User
		err   error
	}
	noticeMsg struct {
		text string
		err  error
	}
	issuesTick struct{}
	agentsTick struct{}
)

// Snapshotter lists agents and live launch records (fleet.Fleet in production).
type Snapshotter interface {
	Snapshot(ctx context.Context) ([]agent.Agent, []state.Record, error)
}

// Tmux is the part of tmux.Client the panel drives.
type Tmux interface {
	NewSession(name, cwd string, env, argv []string) (string, error)
	SetOpt(pane, key, val string) error
	Swap(a, b string) error
	Join(src, dst string, percent int) error
	Select(pane string) error
	SendText(pane, text string) error
	SendKeys(pane string, keys ...string) error
	KillSession(name string) error
	HasSession(name string) bool
	Capture(pane string) (string, error)
}

type Options struct {
	Tracker      tracker.Tracker
	Fleet        Snapshotter
	Adapters     []agent.Adapter
	Store        state.Store
	Tmux         Tmux   // nil when not running inside tmux (read-only board)
	Panel        string // this panel's tmux pane
	Placeholder  string // pane that fills the right slot when no agent is shown
	Config       config.Config
	Assignee     string // tracker argument, e.g. "me"
	Issues       []tracker.Issue
	LinearPoll   time.Duration
	ExternalPoll time.Duration

	Hooks   <-chan hook.Event // events from launched agents; nil = no hooks
	HookBin string            // lanes executable launched agents' hooks call
	Socket  string            // where those hooks connect
	Notify  func(text string) // tmux / OS notification; nil = bell only
	Notice  string            // shown in the footer at start (e.g. why hooks are off)
	Readers Readers           // git / PR / browser access for details; zero = real ones
}

type Model struct {
	opt     Options
	label   string // assignee shown in the header
	issues  []tracker.Issue
	agents  []agent.Agent
	live    []state.Record
	links   state.Links
	users   []tracker.User
	rows    []board.Row
	cursor  int
	offset  int
	width   int
	height  int
	updated time.Time

	issueErr error
	agentErr error
	notice   string
	noticeOK bool

	shown     string // record ID of the agent in the right slot
	shownPane string
	stopping  map[string]bool       // record IDs being stopped; not controllable meanwhile
	agentSeq  int                   // numbers agent refreshes as they start
	agentSeen int                   // newest refresh applied; results older than it are dropped
	launchSeq int                   // refreshes started at or before this predate the last launch
	hooks     map[string]*hookState // by launch record ID

	details      bool // bottom half of the panel shows the selected row's details
	detailSeq    int
	readers      Readers
	gitCache     map[string]cachedGit // by directory
	prCache      map[string]cachedPR  // by directory + "\x00" + branch
	issueDetails map[string]cachedIssue
	modal        *modal

	now func() time.Time
}

func New(opt Options) *Model {
	m := &Model{opt: opt, label: opt.Assignee, issues: opt.Issues, updated: time.Now(), now: time.Now, width: 80, height: 24,
		stopping: map[string]bool{}, hooks: map[string]*hookState{},
		readers: opt.Readers, gitCache: map[string]cachedGit{}, prCache: map[string]cachedPR{},
		issueDetails: map[string]cachedIssue{}}
	if m.readers.Git == nil {
		m.readers = defaultReaders()
	}
	m.notice = opt.Notice
	if l, err := opt.Store.Links(); err == nil {
		m.links = l
	} else {
		m.links, m.notice = state.Links{}, err.Error()
	}
	m.rebuild()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.fetchAgents(), tick(m.opt.LinearPoll, issuesTick{}), tick(m.opt.ExternalPoll, agentsTick{}), m.waitHook())
}

func tick(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

func (m *Model) fetchIssues() tea.Cmd {
	tr, who := m.opt.Tracker, m.opt.Assignee
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		is, err := tr.Issues(ctx, who)
		return issuesMsg{who, is, err}
	}
}

func (m *Model) fetchAgents() tea.Cmd {
	f := m.opt.Fleet
	m.agentSeq++
	seq := m.agentSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a, live, err := f.Snapshot(ctx)
		return agentsMsg{seq, a, live, err}
	}
}

func (m *Model) fetchUsers() tea.Cmd {
	tr := m.opt.Tracker
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		us, err := tr.Users(ctx)
		return usersMsg{us, err}
	}
}

func (m *Model) stateOrder() map[string][]string {
	o := map[string][]string{}
	for team, tc := range m.opt.Config.Teams {
		o[team] = tc.StateOrder
	}
	return o
}

func (m *Model) rebuild() {
	agents := slices.Clone(m.agents)
	m.overlay(agents)
	m.rows = board.Build(m.issues, agents, m.links, m.stateOrder())
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
}

func (m *Model) say(text string, err error) {
	if err != nil {
		m.notice, m.noticeOK = err.Error(), false
	} else {
		m.notice, m.noticeOK = text, true
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case issuesTick:
		return m, tea.Batch(m.fetchIssues(), tick(m.opt.LinearPoll, issuesTick{}))
	case agentsTick:
		return m, tea.Batch(m.fetchAgents(), tick(m.opt.ExternalPoll, agentsTick{}))
	case issuesMsg:
		if msg.assignee != m.opt.Assignee {
			return m, nil // stale: fetched before the assignee changed
		}
		m.issueErr = msg.err
		if msg.err == nil { // keep the last good data on failure
			m.issues, m.updated = msg.issues, m.now()
			m.rebuild()
			return m, m.detailsMoved() // the row under the cursor may have changed
		}
	case agentsMsg:
		if msg.seq < m.agentSeen || (m.launchSeq > 0 && msg.seq <= m.launchSeq) {
			return m, nil // stale: a newer refresh landed, or it predates the last launch
		}
		m.agentSeen = msg.seq
		m.agentErr = msg.err
		if msg.err == nil || len(msg.agents) > 0 {
			m.agents, m.live = msg.agents, msg.live
			m.dropVanishedShown()
			m.forgetStopped()
			m.forgetHooks()
			m.rebuild()
			return m, m.detailsMoved()
		}
	case usersMsg:
		if msg.err != nil {
			m.say("", msg.err)
			if m.modal != nil && m.modal.id == "assignee" {
				m.modal = nil
			}
			return m, nil
		}
		m.users = msg.users
		if m.modal != nil && m.modal.id == "assignee" {
			m.modal.items = m.assigneeChoices()
		}
	case noticeMsg:
		m.say(msg.text, msg.err)
		return m, m.fetchAgents()
	case launchedMsg:
		return m, m.launched(msg)
	case hookMsg:
		return m, tea.Batch(m.hook(hook.Event(msg)), m.waitHook())
	case trustMsg:
		return m, m.trusted(msg)
	case detailTick:
		if msg.seq == m.detailSeq && m.details {
			return m, m.fetchDetails(false)
		}
	case gitMsg, prMsg, issueMsg:
		m.detailsUpdate(msg)
	case trustGoneMsg:
		if st := m.hooks[msg.id]; st != nil {
			st.trust = false
			m.rebuild()
		}
	case detailMsg:
		return m, m.gotDetail(msg)
	case tea.KeyPressMsg:
		if md := m.modal; md != nil {
			if msg.String() == "ctrl+c" {
				m.unshow()
				return m, tea.Quit
			}
			cmd, done := md.key(msg)
			if done && m.modal == md { // a callback may have opened the next modal
				m.modal = nil
			}
			return m, cmd
		}
		return m, m.boardKey(msg)
	}
	return m, nil
}

func (m *Model) boardKey(k tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	switch k.String() {
	case "q", "ctrl+c":
		m.unshow() // best effort on the way out; Reconcile repairs the rest next start
		return tea.Quit
	case "j", "down":
		m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
		return m.detailsMoved()
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
		return m.detailsMoved()
	case "g", "home":
		m.cursor = 0
		return m.detailsMoved()
	case "G", "end":
		m.cursor = max(len(m.rows)-1, 0)
		return m.detailsMoved()
	case "d":
		m.details = !m.details
		if m.details {
			return m.fetchDetails(false)
		}
	case "o":
		return m.openSelected()
	case "r":
		cmds := []tea.Cmd{m.fetchIssues(), m.fetchAgents()}
		if m.details {
			cmds = append(cmds, m.fetchDetails(true))
		}
		return tea.Batch(cmds...)
	case "u":
		m.modal = &modal{id: "assignee", title: "Show tickets assigned to", items: m.assigneeChoices(),
			onChoose: m.chooseAssignee}
		if m.users == nil {
			return m.fetchUsers()
		}
	case "enter":
		return m.focusSelected()
	case "n":
		return m.newAgent()
	case "s":
		return m.sendSelected()
	case "x":
		return m.stopSelected()
	case "l":
		return m.linkSelected()
	case "a":
		return m.approve()
	}
	return nil
}

func (m *Model) assigneeChoices() []choice {
	cs := []choice{{"me", "me"}}
	for _, u := range m.users {
		cs = append(cs, choice{u.Name + " " + faint.Render(u.Email), u.ID + "\x00" + u.Name})
	}
	return cs
}

func (m *Model) chooseAssignee(c choice) tea.Cmd {
	id, name, ok := strings.Cut(c.value, "\x00")
	if !ok {
		name = id
	}
	m.opt.Assignee, m.label = id, name
	m.issues, m.cursor = nil, 0
	m.rebuild()
	return m.fetchIssues()
}

func (m *Model) selected() (board.Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return board.Row{}, false
	}
	return m.rows[m.cursor], true
}

var (
	bold   = lipgloss.NewStyle().Bold(true)
	faint  = lipgloss.NewStyle().Faint(true)
	sel    = lipgloss.NewStyle().Reverse(true)
	errSty = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	stateS = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	workS  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	waitS  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
)

func (m *Model) View() tea.View {
	var b strings.Builder
	header := fmt.Sprintf("lanes · %s · %d tickets · %d agents · updated %s",
		m.label, len(m.issues), len(m.agents), m.updated.Format("15:04:05"))
	if n := m.waitingCount(); n > 0 { // first, so a narrow panel never cuts it off
		w := fmt.Sprintf("⚠ %d waiting (a) · ", n)
		b.WriteString(waitS.Render(trunc(w, m.width)) + bold.Render(trunc(header, max(m.width-len([]rune(w)), 1))) + "\n")
	} else {
		b.WriteString(bold.Render(trunc(header, m.width)) + "\n")
	}

	body := max(m.height-2, 1)
	switch {
	case m.modal != nil:
		m.modal.view(&b, m.width, body)
	case m.details:
		boardH := body / 2
		if boardH < 6 || body-boardH < 8 { // too short to split: details take it all
			boardH = 0
		}
		if boardH > 0 {
			m.viewBoard(&b, boardH)
		}
		b.WriteString(faint.Render(trunc("── details (d hides · o opens · r refreshes) "+strings.Repeat("─", m.width), m.width)) + "\n")
		m.viewDetails(&b, body-boardH-1)
	default:
		m.viewBoard(&b, body)
	}

	switch {
	case m.notice != "" && m.noticeOK:
		b.WriteString(okSty.Render(trunc(m.notice, m.width)))
	case m.notice != "":
		b.WriteString(errSty.Render(trunc(firstLine(m.notice), m.width)))
	case m.issueErr != nil:
		b.WriteString(errSty.Render(trunc("tracker: "+firstLine(m.issueErr.Error()), m.width)))
	case m.agentErr != nil:
		b.WriteString(errSty.Render(trunc("agents: "+firstLine(m.agentErr.Error()), m.width)))
	case m.modal != nil:
		b.WriteString(faint.Render(trunc(m.modal.hint(), m.width)))
	case m.opt.Tmux == nil:
		b.WriteString(faint.Render(trunc("j/k move · d details · r refresh · u assignee · l link · q quit  (run inside tmux to launch agents)", m.width)))
	default:
		b.WriteString(faint.Render(trunc("enter show · d details · a approve · n new · s send · x stop · l link · r refresh · u assignee · q quit", m.width)))
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m *Model) viewBoard(b *strings.Builder, body int) {
	if len(m.rows) == 0 {
		b.WriteString(faint.Render("  no open tickets or agents") + "\n")
		b.WriteString(strings.Repeat("\n", body-1))
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+body {
		m.offset = m.cursor - body + 1
	}
	end := min(m.offset+body, len(m.rows))
	for i := m.offset; i < end; i++ {
		line := trunc(m.line(m.rows[i]), m.width)
		if i == m.cursor {
			line = sel.Render(line)
		} else {
			line = m.style(m.rows[i]).Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(strings.Repeat("\n", body-(end-m.offset)))
}

func (m *Model) style(r board.Row) lipgloss.Style {
	switch r.Kind {
	case board.TeamRow, board.UnlinkedRow:
		return bold
	case board.StateRow:
		return stateS
	case board.AgentRow:
		switch r.Agent.Status {
		case agent.Working:
			return workS
		case agent.Waiting:
			return waitS
		}
		return faint
	}
	return lipgloss.NewStyle()
}

func (m *Model) line(r board.Row) string {
	ind := strings.Repeat("  ", r.Level)
	switch r.Kind {
	case board.StateRow:
		return ind + "── " + r.Text + " ──"
	case board.AgentRow:
		a := r.Agent
		s := fmt.Sprintf("%s%s %s %s %-7s %s", ind, glyph(a.Status), a.Tool, a.Name, a.Status, age(m.now().Sub(a.Since)))
		s += m.waitingNote(a)
		if a.Branch != "" && a.Branch != a.Name {
			s += " · " + a.Branch
		}
		if r.Level == 1 { // unlinked: show where it runs
			s += " · " + home(a.Cwd)
		}
		if a.External {
			s += " (ro)"
		}
		if a.RecordID != "" && a.RecordID == m.shown {
			s += " ◀"
		}
		return s
	}
	return ind + r.Text
}

func glyph(s agent.Status) string {
	switch s {
	case agent.Working:
		return "●"
	case agent.Idle:
		return "○"
	case agent.Waiting:
		return "⚠"
	case agent.Done:
		return "·"
	}
	return "◌"
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func home(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func trunc(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:max(n-1, 0)]) + "…"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
