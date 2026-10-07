package ui

import (
	"fmt"
	"slices"
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

// spinner frames for working agents; m.spin advances while any agent works.
var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (m *Model) glyphOf(st agent.Status) string {
	if st == agent.Working {
		return string(spinner[m.spin%len(spinner)])
	}
	return glyph(st)
}

func (m *Model) header() string {
	left := []seg{s("▐", accentS), s("lanes", badgeS), s("▌", accentS), s("  "+m.label, bold),
		s(fmt.Sprintf(" · %d tickets", len(m.issues)), faint)}
	var work, idle int
	agents := slices.Clone(m.agents)
	m.overlay(agents) // hook status, as on the rows
	for _, a := range agents {
		switch {
		case a.Ended:
		case a.Status == agent.Working:
			work++
		case a.Status == agent.Idle:
			idle++
		}
	}
	left = append(left, s("   ", plainS))
	if work > 0 {
		left = append(left, s(fmt.Sprintf("%s %d  ", m.glyphOf(agent.Working), work), workS))
	}
	if n := m.waitingCount(); n > 0 {
		left = append(left, s(fmt.Sprintf(" ⚠ %d waiting · a ", n), warnBdg), s("  ", plainS))
	}
	if idle > 0 {
		left = append(left, s(fmt.Sprintf("○ %d", idle), faint))
	}
	right := "↻ " + m.updated.Format("15:04")
	gap := m.width - restLen(left) - len([]rune(right))
	if gap < 1 {
		return line(m.width, left...)
	}
	return line(m.width, append(left, s(strings.Repeat(" ", gap), plainS), s(right, faint))...)
}

// rowSegs lays out one board row: a lane rail down the left for each workflow state
// (dashed for sessions on no ticket), tickets in it, agents branching off them.
func (m *Model) rowSegs(i int, st rowStats) []seg {
	r := m.rows[i]
	rail := s(" ", plainS)
	if st.rail != "" {
		rail = s(" "+st.rail, st.railS)
	}
	switch r.Kind {
	case board.TeamRow:
		return []seg{s(" "+r.Text, bold), s(fmt.Sprintf("  %d", st.count), faint)}
	case board.UnlinkedRow:
		return m.laneHead(rail, "UNLINKED", st.count, faint)
	case board.StateRow:
		title := strings.ToUpper(r.Text)
		if isPoolLane(r) {
			title = "▾ " + title
			if !m.poolOpen && m.filter == "" {
				title = "▸ " + title[len("▾ "):]
			}
		}
		return m.laneHead(rail, title, st.count, st.railS)
	case board.TicketRow:
		out := []seg{rail, s("  ", plainS), s(r.Issue.Key, keyS), s(" "+r.Issue.Title, plainS)}
		if st.waiting {
			out = append(out, s("  ⚠", waitS))
		}
		return out
	case board.AgentRow:
		return append([]seg{rail}, m.agentSegs(r, st.last)...)
	}
	return []seg{s(r.Text, plainS)}
}

// laneHead is a lane's title with a rule out to its count: "┃ IN REVIEW ───── 3".
func (m *Model) laneHead(rail seg, title string, n int, st lipgloss.Style) []seg {
	count := fmt.Sprintf(" %d", n)
	rule := max(m.width-restLen([]seg{rail})-len([]rune(title))-len(count)-3, 1)
	return []seg{rail, s(" "+title+" ", st), s(strings.Repeat("─", rule), faint), s(count, faint)}
}

func (m *Model) agentSegs(r board.Row, last bool) []seg {
	a := r.Agent
	tree := "├─ "
	if last {
		tree = "└─ "
	}
	ind := "   "
	if r.Level == 1 { // unlinked: no ticket above to branch off
		ind, tree = "   ", ""
	}
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
	switch {
	case a.Ended:
		status = "ended"
	case a.Status == agent.Waiting:
		status = "needs you"
	case status == "":
		status = string(agent.Unknown)
	}
	out := []seg{s(ind, plainS), s(tree, faint), s(m.glyphOf(a.Status)+" ", st), s(pad(trunc(shown, 16), 16), name), s("  "+pad(status, 9), st)}
	when := ""
	if !a.Since.IsZero() {
		when = age(m.now().Sub(a.Since))
	}
	out = append(out, s(" "+pad(when, 3), faint)) // blank keeps the columns aligned
	if note := m.waitingNote(a); note != "" {
		out = append(out, s(note, waitS))
	}
	if a.Branch != "" && a.Branch != a.Name {
		out = append(out, s("  "+a.Branch, faint))
	}
	if r.Level == 1 && a.Cwd != "" { // unlinked: show where it runs
		out = append(out, s("  "+home(a.Cwd), faint))
	}
	switch {
	case a.RecordID != "" && a.RecordID == m.shown:
		out = append(out, s("  ◀", accentS))
	case a.External && !a.Ended:
		out = append(out, s("  external", italicF))
	}
	if a.Tool != "claude" {
		out = append(out, s("  "+a.Tool, faint))
	}
	return out
}

// pad right-pads text to n columns so names and statuses line up.
func pad(t string, n int) string {
	if w := len([]rune(t)); w < n {
		return t + strings.Repeat(" ", n-w)
	}
	return t
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
	count   int    // team, lane: tickets (or sessions) in it
	waiting bool   // ticket: has an agent waiting for you
	last    bool   // agent: last agent under its ticket / group
	rail    string // the lane rail drawn at the row's left edge ("" = none)
	railS   lipgloss.Style
}

// laneStyle colors a lane by how far along its state is.
func laneStyle(stateType string) lipgloss.Style {
	switch stateType {
	case "started":
		return accentS
	case "triage", "backlog", "unstarted":
		return faint
	}
	return stateC
}

// stats precomputes what row rendering needs from neighbouring rows.
func (m *Model) stats() []rowStats {
	st := make([]rowStats, len(m.rows))
	team, lane := -1, -1
	rail, railS := "", plainS
	for i, r := range m.rows {
		switch r.Kind {
		case board.TeamRow:
			team, lane, rail = i, -1, ""
		case board.StateRow:
			lane, rail, railS = i, "┃", faint
			if isPoolLane(r) { // count them all, also while the lane is closed
				for _, is := range m.issues {
					if is.Pool && team >= 0 && is.Team == m.rows[team].Text {
						st[i].count++
					}
				}
			}
			for _, n := range m.rows[i+1:] { // the lane's color comes from its tickets' state
				if laneHead(n.Kind) {
					break
				}
				if n.Kind == board.TicketRow {
					railS = laneStyle(n.Issue.StateType)
					break
				}
			}
		case board.UnlinkedRow:
			team, lane, rail, railS = -1, i, "┆", faint
		case board.TicketRow:
			if team >= 0 && !r.Issue.Pool { // the team's count is its own tickets
				st[team].count++
			}
			if lane >= 0 && !(r.Issue.Pool && isPoolLane(m.rows[lane])) {
				st[lane].count++
			}
		case board.AgentRow:
			st[i].last = i+1 >= len(m.rows) || m.rows[i+1].Kind != board.AgentRow
			if lane >= 0 && m.rows[lane].Kind == board.UnlinkedRow {
				st[lane].count++
			}
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
		st[i].rail, st[i].railS = rail, railS
	}
	// Bracket each lane: a cap on its heading, a foot on its last row.
	for i, r := range m.rows {
		if st[i].rail == "" {
			continue
		}
		dashed := st[i].rail == "┆"
		end := i+1 >= len(m.rows) || laneHead(m.rows[i+1].Kind)
		switch {
		case laneHead(r.Kind) && dashed:
			st[i].rail = "╭"
		case laneHead(r.Kind):
			st[i].rail = "┏"
		case end && dashed:
			st[i].rail = "╰"
		case end:
			st[i].rail = "┗"
		}
	}
	return st
}

func laneHead(k board.Kind) bool {
	return k == board.StateRow || k == board.UnlinkedRow || k == board.TeamRow
}

// chip draws a footer key as ⟨k⟩.
func chip(k string) []seg { return []seg{s("⟨", faint), s(k, keyS), s("⟩", faint)} }

// footerLine draws "key label" items as key chips with dim labels. footerClick
// mirrors this layout.
func (m *Model) footerLine(items []string) string {
	var parts []seg
	for i, it := range items {
		if i > 0 {
			parts = append(parts, s("  ", plainS))
		}
		k, label, _ := strings.Cut(it, " ")
		if strings.HasPrefix(k, "·") { // a note, not a key
			parts = append(parts, s(it, faint))
			continue
		}
		parts = append(append(parts, chip(k)...), s(" "+label, faint))
	}
	return line(m.width, parts...)
}

// selectedLine raises the selected row: a notch at the left edge and a tinted band.
func (m *Model) selectedLine(parts []seg) string {
	bg := lipgloss.Color("236")
	if m.light {
		bg = lipgloss.Color("254")
	}
	out := []seg{s("▌", accentS.Background(bg))}
	for i, p := range parts {
		if i == 0 {
			p.s = strings.TrimPrefix(p.s, " ") // every row starts with a space; the notch takes it
		}
		out = append(out, s(p.s, p.st.Background(bg)))
	}
	if n := m.width - restLen(out); n > 0 {
		out = append(out, s(strings.Repeat(" ", n), plainS.Background(bg)))
	}
	return line(m.width, out...)
}
