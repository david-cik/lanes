// Package ui is the Bubble Tea board.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/tracker"
)

type (
	issuesMsg struct {
		assignee string
		issues   []tracker.Issue
		err      error
	}
	agentsMsg struct {
		agents []agent.Agent
		err    error
	}
	usersMsg struct {
		users []tracker.User
		err   error
	}
	issuesTick struct{}
	agentsTick struct{}
)

type Options struct {
	Tracker      tracker.Tracker
	Adapters     []agent.Adapter
	Assignee     string // tracker argument, e.g. "me"
	Issues       []tracker.Issue
	LinearPoll   time.Duration
	ExternalPoll time.Duration
}

type Model struct {
	opt      Options
	label    string // assignee shown in the header
	issues   []tracker.Issue
	agents   []agent.Agent
	rows     []board.Row
	cursor   int
	offset   int
	width    int
	height   int
	updated  time.Time
	issueErr error
	agentErr error

	picking bool
	users   []tracker.User
	filter  string
	pick    int

	now func() time.Time
}

func New(opt Options) *Model {
	m := &Model{opt: opt, label: opt.Assignee, issues: opt.Issues, updated: time.Now(), now: time.Now, width: 80, height: 24}
	m.rebuild()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.fetchAgents(), tick(m.opt.LinearPoll, issuesTick{}), tick(m.opt.ExternalPoll, agentsTick{}))
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
	ads := m.opt.Adapters
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var all []agent.Agent
		var errs []error
		for _, a := range ads {
			got, err := a.ListExternal(ctx)
			if err != nil {
				errs = append(errs, err)
			}
			all = append(all, got...)
		}
		agent.FillBranches(ctx, all)
		return agentsMsg{all, errors.Join(errs...)}
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

func (m *Model) rebuild() {
	m.rows = board.Build(m.issues, slices.Clone(m.agents))
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
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
		}
	case agentsMsg:
		m.agentErr = msg.err
		if msg.err == nil || len(msg.agents) > 0 {
			m.agents = msg.agents
			m.rebuild()
		}
	case usersMsg:
		if msg.err != nil {
			m.issueErr, m.picking = msg.err, false
		}
		m.users = msg.users
	case tea.KeyPressMsg:
		if m.picking {
			return m, m.pickKey(msg)
		}
		return m, m.boardKey(msg)
	}
	return m, nil
}

func (m *Model) boardKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "j", "down":
		m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(len(m.rows)-1, 0)
	case "r":
		return tea.Batch(m.fetchIssues(), m.fetchAgents())
	case "u":
		m.picking, m.filter, m.pick = true, "", 0
		if m.users == nil {
			return m.fetchUsers()
		}
	}
	return nil
}

type choice struct{ arg, label string }

func (m *Model) choices() []choice {
	cs := []choice{{"me", "me"}}
	f := strings.ToLower(m.filter)
	for _, u := range m.users {
		if f == "" || strings.Contains(strings.ToLower(u.Name+" "+u.Email), f) {
			cs = append(cs, choice{u.ID, u.Name})
		}
	}
	return cs
}

func (m *Model) pickKey(k tea.KeyPressMsg) tea.Cmd {
	cs := m.choices()
	switch k.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.picking = false
	case "down", "ctrl+n":
		m.pick = min(m.pick+1, len(cs)-1)
	case "up", "ctrl+p":
		m.pick = max(m.pick-1, 0)
	case "enter":
		c := cs[m.pick]
		m.picking, m.opt.Assignee, m.label = false, c.arg, c.label
		m.issues, m.cursor = nil, 0
		m.rebuild()
		return m.fetchIssues()
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter, m.pick = string(r[:len(r)-1]), 0
		}
	default:
		if t := k.Text; t != "" {
			m.filter, m.pick = m.filter+t, 0
		}
	}
	return nil
}

var (
	bold   = lipgloss.NewStyle().Bold(true)
	faint  = lipgloss.NewStyle().Faint(true)
	sel    = lipgloss.NewStyle().Reverse(true)
	errSty = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stateS = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	workS  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

func (m *Model) View() tea.View {
	var b strings.Builder
	header := fmt.Sprintf("lanes · %s · %d tickets · %d agents · updated %s",
		m.label, len(m.issues), len(m.agents), m.updated.Format("15:04:05"))
	b.WriteString(bold.Render(trunc(header, m.width)) + "\n")

	body := max(m.height-2, 1)
	if m.picking {
		m.viewPicker(&b, body)
	} else {
		m.viewBoard(&b, body)
	}

	switch {
	case m.issueErr != nil:
		b.WriteString(errSty.Render(trunc("tracker: "+firstLine(m.issueErr), m.width)))
	case m.agentErr != nil:
		b.WriteString(errSty.Render(trunc("agents: "+firstLine(m.agentErr), m.width)))
	case m.picking:
		b.WriteString(faint.Render("type to filter · ↑/↓ · enter select · esc cancel"))
	default:
		b.WriteString(faint.Render("j/k move · r refresh · u assignee · q quit"))
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

func (m *Model) viewPicker(b *strings.Builder, body int) {
	b.WriteString("assignee: " + m.filter + "▏\n")
	if m.users == nil {
		b.WriteString(faint.Render("  loading users…") + "\n")
		b.WriteString(strings.Repeat("\n", max(body-2, 0)))
		return
	}
	cs := m.choices()
	start := max(m.pick-(body-2), 0)
	end := min(start+body-1, len(cs))
	for i := start; i < end; i++ {
		line := trunc("  "+cs[i].label, m.width)
		if i == m.pick {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(strings.Repeat("\n", max(body-1-(end-start), 0)))
}

func (m *Model) style(r board.Row) lipgloss.Style {
	switch r.Kind {
	case board.TeamRow, board.UnlinkedRow:
		return bold
	case board.StateRow:
		return stateS
	case board.AgentRow:
		if r.Agent.Status == agent.Working {
			return workS
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
		if a.Branch != "" {
			s += " · " + a.Branch
		}
		if r.Level == 1 { // unlinked: show where it runs
			s += " · " + home(a.Cwd)
		}
		if a.External {
			s += " (ro)"
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

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}
