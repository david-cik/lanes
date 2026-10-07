package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

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
	case r.Kind == board.TicketRow:
		keys = append(keys, "enter agent", "n new", "d details", "o open")
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

var helpLines = []string{
	"Board                                  (mouse: click selects, click again opens, wheel moves)",
	"  ↑ ↓  j k     move (the agent pane follows)   ·  g G  Home End  top / bottom",
	"  ctrl+d ctrl+u  PgDn PgUp   half a page",
	"  /          filter tickets and agents as you type (enter keeps it · esc clears)",
	"  enter      agent: go to it · ticket: go to its agent, or resume / start one · ended: resume",
	"  l  →       go to the agent pane",
	"  n          start an agent on the ticket (y start · f other folder · w worktree)",
	"  A          adopt a session lanes didn't start (or resume an ended one)",
	"  s          send text to the agent",
	"  x          stop the agent (worktree and branch are kept)",
	"  t          move a session to another ticket, or take it off its ticket",
	"  T          suggest tickets for unlinked sessions from their transcripts",
	"  :          ask Claude, e.g. \"attach my CSP session to ABC-12\" (you confirm)",
	"  a          answer a waiting permission request",
	"  d          details: git, pull request, activity  ·  o  open PR / ticket",
	"  u          show someone else's tickets  ·  r  refresh  ·  q  quit",
	"",
	"Panes",
	"  {focus} or click   jump between the board and the agent  ·  {prefix} z  zoom the agent",
	"",
	"Approving (a)",
	"  y once · s this session · A always (this repo) · e edit rule · tab other rule",
	"  n deny · m deny with message · p answer in the pane",
}

func (m *Model) showHelp() {
	var lines []string
	for _, l := range helpLines {
		if strings.Contains(l, "{focus}") {
			if m.opt.FocusLabel == "" {
				l = "  l → or click   jump between the board and the agent  ·  {prefix} z  zoom the agent"
			} else {
				l = strings.Replace(l, "{focus}", strings.TrimSuffix(m.opt.FocusLabel, " ⇄"), 1)
			}
			l = strings.Replace(l, "{prefix}", m.opt.Prefix, 1)
			lines = append(lines, l)
			if strings.HasPrefix(m.opt.FocusLabel, "Ctrl-b") {
				lines = append(lines, "  (in an agent, Ctrl-b twice sends Ctrl-b to Claude, which moves the session to the background)")
			}
			continue
		}
		lines = append(lines, l)
	}
	closeHelp := func() (tea.Cmd, bool) { return nil, false }
	m.modal = &modal{title: "Keys", lines: lines, actions: []action{
		{"?", "close", closeHelp}, {"q", "close", closeHelp}, {"enter", "close", closeHelp},
	}}
}

// focusItem turns "Ctrl-b ←/→" into a footer item whose key part has no spaces.
func focusItem(label string) string {
	return strings.Replace(label, " ", "+", 1) + " panes"
}
