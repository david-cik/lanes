package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/board"
)

// footer lists only the keys that do something for the selected row; ? lists them all.
func (m *Model) footer() string {
	var keys []string
	if m.waitingCount() > 0 {
		keys = append(keys, "a approve")
	}
	r, ok := m.selected()
	switch {
	case !ok:
	case r.Kind == board.TicketRow:
		keys = append(keys, "enter agent", "n new", "d details", "o open")
	case r.Kind == board.AgentRow && r.Agent.Ended:
		keys = append(keys, "enter resume", "l ticket", "d details")
	case r.Kind == board.AgentRow && r.Agent.RecordID != "":
		keys = append(keys, "enter show", "s send", "x stop", "l ticket", "d details")
	case r.Kind == board.AgentRow:
		keys = append(keys, "A adopt", "l ticket", "d details")
	}
	if m.opt.FocusLabel != "" {
		keys = append(keys, m.opt.FocusLabel+" ⇄")
	}
	keys = append(keys, "? keys") // q quit and the rest are in ?
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += " · "
		}
		out += k
	}
	return out
}

var helpLines = []string{
	"Board",
	"  j k g G    move",
	"  enter      agent: show it · ticket: show/resume/start its agent · ended: resume",
	"  n          start an agent on the ticket (y start · f other folder · w worktree)",
	"  A          adopt a session lanes didn't start (or resume an ended one)",
	"  s          send text to the agent",
	"  x          stop the agent (worktree and branch are kept)",
	"  l          move a session to another ticket, or remove it from its ticket",
	"  L          suggest tickets for unlinked sessions from their transcripts",
	"  a          answer a waiting permission request",
	"  d          details: git, pull request, activity  ·  o  open PR / ticket",
	"  u          show someone else's tickets  ·  r  refresh  ·  q  quit",
	"",
	"Panes",
	"  Ctrl-] or click   jump between the board and the agent",
	"",
	"Approving (a)",
	"  y once · s this session · A always (this repo) · e edit rule · tab other rule",
	"  n deny · m deny with message · p answer in the pane",
}

func (m *Model) showHelp() {
	lines := append([]string{}, helpLines...)
	for i, l := range lines {
		if strings.HasPrefix(l, "  Ctrl-] or click") {
			if m.opt.FocusLabel == "" {
				lines[i] = "  click      jump between the board and the agent"
			} else {
				lines[i] = strings.Replace(l, "Ctrl-]", m.opt.FocusLabel, 1)
			}
		}
	}
	closeHelp := func() (tea.Cmd, bool) { return nil, false }
	m.modal = &modal{title: "Keys", lines: lines, actions: []action{
		{"?", "close", closeHelp}, {"q", "close", closeHelp}, {"enter", "close", closeHelp},
	}}
}
