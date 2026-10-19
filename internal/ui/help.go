package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
)

// footer lists only the keys that do something for the selected row; ? lists them all.
func (m *Model) footer() string { return strings.Join(m.footerItems(), " · ") }

func (m *Model) footerItems() []string {
	if m.opt.Tmux == nil {
		return []string{"d details", "t ticket", "T suggest", "u assignee", "? keys", "· read-only (start lanes in tmux to launch agents)"}
	}
	var keys []string
	if m.filter != "" {
		keys = append(keys, "esc clear /"+m.filter)
	}
	if m.waitingCount() > 0 {
		keys = append(keys, "a approve")
	}
	r, ok := m.selected()
	switch {
	case !ok:
	case r.Kind == board.TeamRow && m.folded[r.Text]:
		keys = append(keys, "enter unfold", "] next")
	case r.Kind == board.TeamRow:
		keys = append(keys, "enter fold", "] next")
	case isPoolLane(r) && m.poolOpen:
		keys = append(keys, "enter hide")
	case isPoolLane(r):
		keys = append(keys, "enter show")
	case r.Kind == board.TicketRow && r.Issue.Pool:
		keys = append(keys, "enter start", "d details", "o open")
	case r.Kind == board.TicketRow:
		keys = append(keys, "enter agent", "n new", "i info", "p PRs", "o open")
	case r.Kind == board.AgentRow && r.Agent.Ended:
		keys = append(keys, "enter resume", "t ticket", "d details")
	case r.Kind == board.AgentRow && r.Agent.RecordID != "":
		keys = append(keys, "enter go to", "s send", "x stop", "t ticket", "d details")
	case r.Kind == board.AgentRow:
		keys = append(keys, "A adopt", "t ticket", "d details")
	}
	if m.opt.FocusLabel != "" {
		keys = append(keys, focusItem(m.opt.FocusLabel))
	}
	keys = append(keys, "? keys") // q quit and the rest are in ?
	return keys
}

// helpSection is one block of the key list: a title and key → what it does.
type helpSection struct {
	title string
	keys  [][2]string
}

var helpSections = []helpSection{
	{"Move", [][2]string{
		{"↑ ↓  j k", "move (the agent shows on the right)"},
		{"g G", "top / bottom"},
		{"PgUp PgDn", "half a page (also ctrl+u ctrl+d)"},
		{"[ ]", "previous / next team"},
		{"/", "filter as you type (esc clears)"},
		{"click", "select (again: ticket card, fold); wheel scrolls"},
	}},
	{"Look", [][2]string{
		{"enter", "go to the agent (on a ticket: its agent)"},
		{"i", "the ticket: state, PRs, description"},
		{"o", "open the ticket in the browser"},
		{"p", "the ticket's pull requests"},
		{"d", "details under the board (git, PR, activity)"},
	}},
	{"Work", [][2]string{
		{"n", "start an agent on the ticket"},
		{"N", "new ticket, drafted by Claude (from a session too)"},
		{"s", "send text to the agent"},
		{"a", "answer a waiting request"},
		{"x", "stop the agent (worktree is kept)"},
		{"A", "adopt a session lanes didn't start"},
		{"C", "the project's coordinator: an agent that runs the others"},
	}},
	{"Sessions on tickets", [][2]string{
		{"t", "move a session to a ticket, or take it off"},
		{"T", "suggest tickets for unlinked sessions"},
		{":", "ask Claude (\"find my old sessions\")"},
	}},
	{"Board", [][2]string{
		{"enter", "on a team: fold it to one line"},
		{"enter", "on Up for grabs: show unassigned tickets"},
		{"u", "someone else's tickets"},
		{"r", "refresh"},
		{"q", "quit (agents keep running)"},
	}},
	{"Panes", nil}, // filled in by panesHelp: it depends on your tmux prefix
	{"Starting an agent (n)", [][2]string{
		{"y  enter", "start"},
		{"f", "start in another folder"},
		{"w", "use a new git worktree instead"},
		{"c", "up-for-grabs ticket: claim it, or not"},
	}},
	{"Approving (a)", [][2]string{
		{"y", "allow once"},
		{"s", "allow for this session"},
		{"A", "always allow (this repo)"},
		{"e  tab", "edit the rule / pick another rule"},
		{"n", "deny"},
		{"m", "deny with a message"},
		{"p", "answer in the agent's pane"},
	}},
}

// divider is a section title ruled out to the panel's edge: "── Move ──────".
func (m *Model) divider(title string) string {
	head := "── " + title + " "
	return sect.Render(head) + faint.Render(strings.Repeat("─", max(m.width-len([]rune(head)), 3)))
}

// helpKeyWidth is the key column of the key list.
const helpKeyWidth = 11

func helpRow(k, d string) string {
	return "  " + keyS.Render(pad(k, helpKeyWidth)) + " " + d
}

func (m *Model) showHelp() {
	var lines []string
	for _, sec := range helpSections {
		lines = append(lines, m.divider(sec.title))
		keys := sec.keys
		if sec.title == "Panes" {
			keys = m.panesHelp()
		}
		for _, k := range keys {
			lines = append(lines, helpRow(k[0], k[1]))
		}
	}
	lines = append(lines, m.divider("Status"),
		"  "+workS.Render(m.glyphOf(agent.Working))+" working   "+okSty.Render("✓")+" ready   ○ idle   "+waitS.Render("⚠")+" needs you   · ended", "")
	closeHelp := func() (tea.Cmd, bool) { return nil, false }
	m.modal = &modal{title: "Keys", lines: lines, hint: "↑↓ scroll · ? esc close", actions: []action{
		{"?", "", closeHelp}, {"q", "", closeHelp}, {"enter", "", closeHelp}, // the hint says how
	}}
}

// panesHelp lists the keys for moving between the board and the agent.
func (m *Model) panesHelp() [][2]string {
	p := m.opt.Prefix
	var rows [][2]string
	if f := strings.TrimSuffix(m.opt.FocusLabel, " ⇄"); f != "" {
		rows = append(rows, [2]string{f, "board ⇄ agent"})
	}
	rows = append(rows,
		[2]string{"l  →", "from the board, go to the agent"},
		[2]string{"click", "a pane to move into it"},
		[2]string{p + " z", "zoom the agent pane"},
	)
	switch {
	case m.opt.DoublePrefix:
		rows = append(rows, [2]string{p + " " + p, "also board ⇄ agent"})
	case p == "Ctrl-b":
		rows = append(rows, [2]string{"Ctrl-b ×2", "avoid in an agent: Claude backgrounds"})
	}
	return rows
}

// focusItem turns "Ctrl-b ←/→" into a footer item whose key part has no spaces.
func focusItem(label string) string {
	return strings.Replace(label, " ", "+", 1) + " panes"
}
