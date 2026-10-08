// Package ui is the Bubble Tea board.
package ui

import (
	"context"
	"fmt"
	"maps"
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
		poolErr  error // the assigned tickets loaded but the unassigned ones didn't
	}
	agentsMsg struct {
		seq    int
		agents []agent.Agent
		live   []state.Record
		err    error
		links  state.Links // re-read from disk: `lanes link` may have changed them
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

	Hooks      <-chan hook.Event // events from launched agents; nil = no hooks
	HookBin    string            // lanes executable launched agents' hooks call
	Socket     string            // where those hooks connect
	Notify     func(text string) // tmux / OS notification; nil = bell only
	Notice     string            // shown in the footer at start (e.g. why hooks are off)
	Readers    Readers           // git / PR / browser access for details; zero = real ones
	FocusLabel string            // key that jumps between board and agent, e.g. "Ctrl-]"; "" = none
	Prefix     string            // the tmux prefix as people write it, e.g. "Ctrl-b"
	// DoublePrefix: the prefix pressed twice switches board ⇄ agent here instead of
	// reaching the agent (where Claude takes Ctrl-b as "move to the background").
	DoublePrefix bool
}

type Model struct {
	opt         Options
	label       string // assignee shown in the header
	issues      []tracker.Issue
	agents      []agent.Agent
	live        []state.Record
	links       state.Links
	users       []tracker.User
	rows        []board.Row
	cursor      int
	offset      int
	spin        int               // spinner frame for working agents
	spinOn      bool              // a spinner tick is scheduled
	light       bool              // the terminal has a light background
	filter      string            // board rows narrowed to those matching it (/)
	poolOpen    bool              // the "up for grabs" lanes show their tickets
	folded      map[string]bool   // teams folded down to their header
	poolFailing bool              // the last up-for-grabs fetch failed (already reported)
	teamWork    map[string][2]int // per team: agents working, waiting (for folded headers)
	typing      bool              // the filter is being typed
	width       int
	height      int
	updated     time.Time

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

	adopting    agent.Agent         // session being adopted or resumed (its plan is built off the loop)
	review      *review             // suggested-links screen, when open
	autoChecked map[string]autoSeen // when auto_link last looked at a session
	modal       *modal

	now func() time.Time
}

func New(opt Options) *Model {
	m := &Model{opt: opt, label: opt.Assignee, issues: opt.Issues, updated: time.Now(), now: time.Now, width: 80, height: 24,
		stopping: map[string]bool{}, hooks: map[string]*hookState{},
		readers: opt.Readers, gitCache: map[string]cachedGit{}, prCache: map[string]cachedPR{},
		issueDetails: map[string]cachedIssue{}, autoChecked: map[string]autoSeen{}}
	if m.readers.Git == nil {
		m.readers = defaultReaders()
	}
	if m.readers.Ask == nil {
		m.readers.Ask = claudeAsk("sonnet")
	}
	if m.opt.Prefix == "" {
		m.opt.Prefix = "Ctrl-b"
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
	cmds := []tea.Cmd{m.fetchAgents(), tick(m.opt.LinearPoll, issuesTick{}), tick(m.opt.ExternalPoll, agentsTick{}), m.waitHook(),
		tea.RequestBackgroundColor}
	if _, ok := m.opt.Tracker.(tracker.Pooler); ok {
		cmds = append(cmds, m.fetchIssues()) // the tickets passed in are only the assigned ones
	}
	return tea.Batch(cmds...)
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
		if p, ok := tr.(tracker.Pooler); ok && err == nil {
			var teams []string
			for _, i := range is {
				if !slices.Contains(teams, i.Team) {
					teams = append(teams, i.Team)
				}
			}
			pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second) // its own budget
			defer pcancel()
			pool, perr := p.Pool(pctx, teams)
			if perr != nil { // the assigned tickets still show; the last good pool stays
				return issuesMsg{who, is, nil, fmt.Errorf("up for grabs: %w", perr)}
			}
			is = append(is, pool...)
		}
		return issuesMsg{who, is, err, nil}
	}
}

func (m *Model) fetchAgents() tea.Cmd {
	f := m.opt.Fleet
	m.agentSeq++
	seq := m.agentSeq
	store := m.opt.Store
	fallback := maps.Clone(m.links)
	endedFor := time.Duration(m.opt.Config.EndedDays) * 24 * time.Hour
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		links, lerr := store.Links()
		if lerr != nil {
			links = fallback
		}
		a, live, err := f.Snapshot(ctx)
		a = dropToolSessions(a)
		if err == nil || len(a) > 0 {
			a = append(a, endedSessions(links, a, endedFor)...)
		}
		return agentsMsg{seq: seq, agents: a, live: live, err: err, links: links}
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
	var was string // what the cursor is on, so it can follow it to wherever it moves
	if ids := rowIDs(m.rows); m.cursor < len(ids) {
		was = ids[m.cursor]
	}
	agents := slices.Clone(m.agents)
	m.overlay(agents)
	m.rows = filterRows(board.Build(m.issues, agents, m.links, m.stateOrder()), m.filter)
	if !m.poolOpen && m.filter == "" { // a filter searches the pool too
		m.rows = collapsePool(m.rows)
	}
	if m.opt.Config.GroupByProject {
		m.rows = board.ByProject(m.rows)
	}
	m.teamWork = teamWork(m.rows)
	if m.filter == "" { // a filter searches folded teams too
		m.rows = foldTeams(m.rows, m.folded)
	}
	if i := slices.Index(rowIDs(m.rows), was); was != "" && i >= 0 {
		m.cursor = i
	}
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
}

func (m *Model) say(text string, err error) {
	if err != nil {
		m.notice, m.noticeOK = err.Error(), false
	} else {
		m.notice, m.noticeOK = text, true
	}
}

type spinTick struct{}

// Update handles a message, then keeps the spinner turning while any agent works.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	mm, cmd := m.update(msg)
	if !m.spinOn && m.working() > 0 {
		m.spinOn = true
		cmd = tea.Batch(cmd, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinTick{} }))
	}
	return mm, cmd
}

// working counts running agents that are working, hook status included.
func (m *Model) working() int {
	agents := slices.Clone(m.agents)
	m.overlay(agents)
	n := 0
	for _, a := range agents {
		if a.Status == agent.Working && !a.Ended {
			n++
		}
	}
	return n
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinTick:
		m.spin++
		m.spinOn = false
	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
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
			if msg.poolErr != nil { // keep the up-for-grabs tickets from the last good fetch
				fresh := map[string]bool{}
				for _, i := range msg.issues {
					fresh[i.Key] = true
				}
				for _, i := range m.issues {
					if i.Pool && !fresh[i.Key] { // a claimed one is in the fresh list as yours
						msg.issues = append(msg.issues, i)
					}
				}
				if !m.poolFailing { // say it once, not every refresh
					m.say("", msg.poolErr)
				}
			}
			m.poolFailing = msg.poolErr != nil
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
			m.agents, m.live = dropAdopted(msg.agents, msg.live), msg.live
			if msg.links != nil {
				m.links = msg.links
			}
			m.dropVanishedShown()
			m.forgetStopped()
			m.forgetHooks()
			m.rebuild()
			return m, tea.Batch(m.detailsMoved(), m.autoLink())
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
	case planMsg:
		return m, m.confirmLaunch(msg.plan)
	case askMsg:
		return m, m.gotAsk(msg)
	case claimedMsg:
		return m, m.claimed(msg)
	case adoptPlanMsg:
		return m, m.confirmAdopt(msg.plan, msg.name)
	case suggestMsg:
		m.gotSuggestions(review(msg))
	case autoLinkMsg:
		m.gotAutoLink(msg)
	case tea.MouseClickMsg:
		return m, m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		return m, m.wheel(tea.Mouse(msg))
	case tea.KeyPressMsg:
		if m.review != nil {
			return m, m.reviewKey(msg)
		}
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
	if m.typing {
		if cmd, ok := m.filterKey(k); ok {
			return cmd
		}
	}
	switch k.String() {
	case "q", "ctrl+c":
		m.unshow() // best effort on the way out; Reconcile repairs the rest next start
		return tea.Quit
	case "j", "down":
		return m.moveTo(m.cursor + 1)
	case "k", "up":
		return m.moveTo(m.cursor - 1)
	case "g", "home":
		return m.moveTo(0)
	case "G", "end":
		return m.moveTo(len(m.rows) - 1)
	case "ctrl+d", "pgdown":
		return m.moveTo(m.cursor + m.boardHeight()/2)
	case "ctrl+u", "pgup":
		return m.moveTo(m.cursor - m.boardHeight()/2)
	case "[", "]":
		return m.jumpTeam(k.String() == "]")
	case "l", "right":
		return m.focusAgent()
	case "/":
		m.typing = true
	case "esc":
		if m.filter != "" {
			m.setFilter("")
		}
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
	case "t":
		return m.linkSelected()
	case "a":
		return m.approve()
	case "?":
		m.showHelp()
	case "A":
		return m.adoptSelected()
	case "T":
		return m.suggestLinks()
	case ":":
		return m.ask()
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
	m.issues, m.cursor, m.poolFailing = nil, 0, false
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
	b.WriteString(m.header() + "\n")

	body := max(m.height-2, 1)
	switch {
	case m.review != nil:
		m.viewReview(&b, body)
	case m.modal != nil:
		m.modal.view(&b, m.width, body)
	case m.details:
		boardH := m.boardHeight()
		if boardH > 0 {
			m.viewBoard(&b, boardH)
		}
		b.WriteString(line(m.width, s("▍", accentS), s(" details", bold), s("   d hides · o opens · r refreshes", faint)) + "\n")
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
	case m.review != nil:
		b.WriteString(faint.Render(trunc("j/k move · space toggle · tab other ticket · enter link checked · esc cancel", m.width)))
	case m.modal != nil:
		b.WriteString(faint.Render(trunc(m.modal.help(), m.width)))
	case m.typing:
		b.WriteString(line(m.width, s("/", keyS), s(m.filter+"▏", plainS), s("   enter keep · esc clear · ↑↓ move", faint)))
	default:
		b.WriteString(m.footerLine(m.footerItems()))
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
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
	stats := m.stats()
	for i := m.offset; i < end; i++ {
		parts := m.rowSegs(i, stats[i])
		if i == m.cursor {
			b.WriteString(m.selectedLine(parts) + "\n")
		} else {
			b.WriteString(line(m.width, parts...) + "\n")
		}
	}
	b.WriteString(strings.Repeat("\n", body-(end-m.offset)))
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
