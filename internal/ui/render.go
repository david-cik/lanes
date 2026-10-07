package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
)

// Palette: the terminal's own 16 colors, so lanes follows the user's light/dark theme.
var (
	accentS = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	badgeS  = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12")).Bold(true)
	warnBdg = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3")).Bold(true)
	keyS    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	stateC  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	plainS  = lipgloss.NewStyle()
	italicF = lipgloss.NewStyle().Faint(true).Italic(true)
)

// seg is a piece of a line with its style; lines are built from segments so they can
// be cut to the panel width without slicing through color codes.
type seg struct {
	s  string
	st lipgloss.Style
}

func s(text string, st lipgloss.Style) seg { return seg{text, st} }

// line renders segments cut to width (an ellipsis replaces what doesn't fit).
func line(width int, parts ...seg) string {
	var b strings.Builder
	left := width
	for i, p := range parts {
		r := []rune(clean(p.s))
		if left <= 0 {
			break
		}
		if len(r) > left || (len(r) == left && i < len(parts)-1 && restLen(parts[i+1:]) > 0) {
			cut := max(left-1, 0)
			b.WriteString(p.st.Render(string(r[:cut]) + "…"))
			return b.String()
		}
		b.WriteString(p.st.Render(string(r)))
		left -= len(r)
	}
	return b.String()
}

func restLen(parts []seg) int {
	n := 0
	for _, p := range parts {
		n += len([]rune(p.s))
	}
	return n
}

// plainLine is the same line without styles, padded to width (for the selection bar).
func plainLine(width int, parts ...seg) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.s)
	}
	t := trunc(clean(b.String()), width)
	if pad := width - len([]rune(t)); pad > 0 {
		t += strings.Repeat(" ", pad)
	}
	return t
}

func (m *Model) header() string {
	left := []seg{s(" lanes ", badgeS)}
	if n := m.waitingCount(); n > 0 {
		left = append(left, s(" ", plainS), s(fmt.Sprintf(" ⚠ %d waiting · a ", n), warnBdg))
	}
	left = append(left,
		s("  "+m.label, bold),
		s(fmt.Sprintf(" · %d tickets · %d agents", len(m.issues), len(m.agents)), faint))
	right := "↻ " + m.updated.Format("15:04")
	gap := m.width - restLen(left) - len([]rune(right))
	if gap < 1 {
		return line(m.width, left...)
	}
	return line(m.width, append(left, s(strings.Repeat(" ", gap), plainS), s(right, faint))...)
}

// rowSegs lays out one board row. stats holds per-row counts computed by viewBoard.
func (m *Model) rowSegs(i int, stats rowStats) []seg {
	r := m.rows[i]
	switch r.Kind {
	case board.TeamRow:
		return []seg{s("▍", accentS), s(" "+r.Text, bold), s(fmt.Sprintf("  %d", stats.count), faint)}
	case board.UnlinkedRow:
		return []seg{s("▍", faint), s(" "+r.Text, bold), s("  sessions on no open ticket", faint)}
	case board.StateRow:
		return []seg{s("  "+r.Text, stateC), s(fmt.Sprintf("  %d", stats.count), faint)}
	case board.TicketRow:
		out := []seg{s("    ", plainS), s(r.Issue.Key, keyS), s(" "+r.Issue.Title, plainS)}
		if stats.waiting {
			out = append(out, s("  ⚠", waitS))
		}
		return out
	case board.AgentRow:
		return m.agentSegs(r, stats.last)
	}
	return []seg{s(r.Text, plainS)}
}

func (m *Model) agentSegs(r board.Row, last bool) []seg {
	a := r.Agent
	tree := "├ "
	if last {
		tree = "└ "
	}
	ind := strings.Repeat("  ", r.Level)
	st := statusStyle(a)
	name := plainS
	if a.RecordID != "" && a.RecordID == m.shown {
		name = bold
	}
	shown := a.Name
	if r.Level > 1 && a.TicketKey != "" { // under its ticket: the key is already on the line above
		if t := strings.TrimSpace(strings.TrimPrefix(a.Name, a.TicketKey)); t != a.Name {
			shown = t
		}
		if shown == "" {
			shown = "agent"
		}
	}
	status := string(a.Status)
	if status == "" {
		status = string(agent.Unknown)
	}
	out := []seg{s(ind, plainS), s(tree, faint), s(glyph(a.Status)+" ", st), s(shown, name), s("  "+status, st)}
	if !a.Since.IsZero() {
		out = append(out, s(" "+age(m.now().Sub(a.Since)), faint))
	}
	if note := m.waitingNote(a); note != "" {
		out = append(out, s(note, waitS))
	}
	if a.Branch != "" && a.Branch != a.Name {
		out = append(out, s(" · "+a.Branch, faint))
	}
	if r.Level == 1 && a.Cwd != "" { // unlinked: show where it runs
		out = append(out, s(" · "+home(a.Cwd), faint))
	}
	switch {
	case a.RecordID != "" && a.RecordID == m.shown:
		out = append(out, s("  ◀ shown", accentS))
	case a.Ended:
		out = append(out, s("  ended · enter resumes", italicF))
	case a.External:
		out = append(out, s("  external", italicF))
	}
	if a.Tool != "claude" {
		out = append(out, s("  "+a.Tool, faint))
	}
	return out
}

func statusStyle(a *agent.Agent) lipgloss.Style {
	switch a.Status {
	case agent.Working:
		return workS
	case agent.Waiting:
		return waitS
	}
	return faint
}

type rowStats struct {
	count   int  // team: tickets in it; state: tickets in it
	waiting bool // ticket: has an agent waiting for you
	last    bool // agent: last agent under its ticket / group
}

// stats precomputes what row rendering needs from neighbouring rows.
func (m *Model) stats() []rowStats {
	st := make([]rowStats, len(m.rows))
	team, state := -1, -1
	for i, r := range m.rows {
		switch r.Kind {
		case board.TeamRow:
			team, state = i, -1
		case board.StateRow:
			state = i
		case board.UnlinkedRow:
			team, state = -1, -1
		case board.TicketRow:
			if team >= 0 {
				st[team].count++
			}
			if state >= 0 {
				st[state].count++
			}
		case board.AgentRow:
			st[i].last = i+1 >= len(m.rows) || m.rows[i+1].Kind != board.AgentRow
			if r.Agent.Status == agent.Waiting {
				for j := i - 1; j >= 0; j-- { // flag its ticket
					if m.rows[j].Kind == board.TicketRow {
						st[j].waiting = true
						break
					}
					if m.rows[j].Kind != board.AgentRow {
						break
					}
				}
			}
		}
	}
	return st
}

// footerLine styles "key label · key label" items: keys bright, labels dim.
func (m *Model) footerLine(items []string) string {
	var parts []seg
	for i, it := range items {
		if i > 0 {
			parts = append(parts, s("  ", plainS))
		}
		k, label, _ := strings.Cut(it, " ")
		parts = append(parts, s(k, keyS), s(" "+label, faint))
	}
	return line(m.width, parts...)
}
