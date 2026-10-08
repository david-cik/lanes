package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/state"
)

// moveTo puts the cursor on row i (clamped); the right pane follows the selection.
func (m *Model) moveTo(i int) tea.Cmd {
	m.cursor = max(min(i, len(m.rows)-1), 0)
	if a := m.previewable(); a != nil {
		m.show(a.RecordID, a.Pane)
	}
	return m.detailsMoved()
}

// previewable is the running lanes agent for the selected row: the agent itself, or
// a ticket's first one. nil when there is none.
func (m *Model) previewable() *agent.Agent {
	r, ok := m.selected()
	if !ok || m.opt.Tmux == nil {
		return nil
	}
	usable := func(a *agent.Agent) bool { return a.RecordID != "" && !a.Ended && !m.stopping[a.RecordID] }
	switch r.Kind {
	case board.AgentRow:
		if usable(r.Agent) {
			return r.Agent
		}
	case board.TicketRow:
		for i := range m.agents {
			if a := &m.agents[i]; a.TicketKey == r.Issue.Key && usable(a) {
				return a
			}
		}
	}
	return nil
}

// focusAgent (l, →) moves to the agent pane, like herdr's right arrow.
func (m *Model) focusAgent() tea.Cmd {
	if a := m.previewable(); a != nil {
		m.focus(a.RecordID, a.Pane)
		return nil
	}
	if r, ok := m.selected(); ok && r.Kind == board.TicketRow {
		m.say("", fmt.Errorf("no agent is running on %s (enter starts one)", r.Issue.Key))
		return nil
	}
	m.controllable() // says why
	return nil
}

// boardHeight is how many board rows fit; 0 when details fill the panel.
func (m *Model) boardHeight() int {
	body := max(m.height-2, 1)
	if !m.details {
		return body
	}
	if h := body / 2; h >= 6 && body-h >= 8 {
		return h
	}
	return 0 // too short to split: details take it all
}

// --- filter (/) ---

func (m *Model) filterKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "enter":
		m.typing = false
	case "esc":
		m.typing = false
		m.setFilter("")
	case "backspace":
		m.setFilter(dropLast(m.filter))
	case "up", "down", "ctrl+c":
		return nil, false // move (or quit) while typing
	default:
		if k.Text == "" {
			return nil, false
		}
		m.setFilter(m.filter + k.Text)
	}
	return nil, true
}

func (m *Model) setFilter(f string) {
	m.filter = f
	m.rebuild()
	m.cursor = 0
	for i, r := range m.rows { // land on the first match, not a heading
		if r.Kind == board.TicketRow || r.Kind == board.AgentRow {
			m.cursor = i
			break
		}
	}
}

// filterRows keeps tickets and agents whose text contains f (any case), a matching
// ticket's agents, a matching agent's ticket, and the headings above what's kept.
func filterRows(rows []board.Row, f string) []board.Row {
	if f == "" {
		return rows
	}
	f = strings.ToLower(f)
	keep := make([]bool, len(rows))
	ticket := -1 // the ticket the current agents sit under
	for i, r := range rows {
		switch r.Kind {
		case board.TicketRow:
			ticket = i
			keep[i] = strings.Contains(strings.ToLower(r.Issue.Key+" "+r.Issue.Title), f)
		case board.AgentRow:
			a := r.Agent
			keep[i] = strings.Contains(strings.ToLower(strings.Join([]string{a.Name, a.Branch, a.Cwd, a.Tool, a.ID}, " ")), f)
			if ticket >= 0 && keep[ticket] && r.Level > rows[ticket].Level {
				keep[i] = true
			}
			if keep[i] && ticket >= 0 && r.Level > rows[ticket].Level {
				keep[ticket] = true
			}
		default:
			ticket = -1
		}
	}
	for i, r := range rows { // a heading stays if anything under it does
		if r.Kind == board.TicketRow || r.Kind == board.AgentRow {
			continue
		}
		for j := i + 1; j < len(rows) && !keep[i]; j++ {
			if h := rows[j]; (h.Kind != board.TicketRow && h.Kind != board.AgentRow) && h.Level <= r.Level {
				break
			}
			keep[i] = keep[j]
		}
	}
	var out []board.Row
	for i, r := range rows {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

// --- mouse ---

// click: a row selects it, the selected row opens it (like enter), a footer key
// presses it, a modal's choice picks it.
func (m *Model) click(ms tea.Mouse) tea.Cmd {
	if ms.Button != tea.MouseLeft || m.review != nil {
		return nil
	}
	if m.modal != nil {
		if k, ok := m.modal.keyAt(ms.Y-1, m.width, max(m.height-2, 1)); ok {
			_, cmd := m.Update(k)
			return cmd
		}
		return nil
	}
	if ms.Y == m.height-1 {
		return m.footerClick(ms.X)
	}
	y := ms.Y - 1 // below the header
	if y < 0 || y >= m.boardHeight() || m.offset+y >= len(m.rows) {
		return nil
	}
	if i := m.offset + y; i != m.cursor {
		return m.moveTo(i)
	}
	m.notice = ""
	return m.focusSelected()
}

func (m *Model) wheel(ms tea.Mouse) tea.Cmd {
	step := map[tea.MouseButton]string{tea.MouseWheelUp: "up", tea.MouseWheelDown: "down"}[ms.Button]
	if step == "" || m.review != nil {
		return nil
	}
	_, cmd := m.Update(keyOf(step))
	return cmd
}

func (m *Model) footerClick(x int) tea.Cmd {
	if m.typing || m.notice != "" || m.issueErr != nil || m.agentErr != nil {
		return nil // the footer shows text, not keys
	}
	at := 0
	for _, it := range m.footerItems() {
		k, label, _ := strings.Cut(it, " ")
		w := lipgloss.Width(k) + 2 + 1 + lipgloss.Width(label) // ⟨k⟩ label
		if x >= at && x < at+w {
			if strings.Contains(k, "+") { // the tmux pane keys: clicking means "go to the agent"
				return m.focusAgent()
			}
			return m.boardKey(keyOf(k))
		}
		at += w + 2
	}
	return nil
}

// keyOf builds the key press a footer label or a mouse gesture stands for.
func keyOf(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// keyAt maps a click on line y of a modal (0 = its title) to the key that does the
// same: a list item (after selecting it), an action, or confirm's yes / no.
func (md *modal) keyAt(y, width, body int) (tea.KeyPressMsg, bool) {
	head := len(md.head(width))
	y -= head
	switch {
	case y < 0 || md.input:
	case md.actions != nil:
		if y < len(md.actions) {
			return keyOf(md.actions[y].key), true
		}
	case md.confirm:
		if y < 2 {
			return keyOf([]string{"y", "n"}[y]), true
		}
	default:
		y-- // the filter line
		vis := md.visible()
		room := max(body-(head+1), 1)
		if i := max(md.pick-room+1, 0) + y; y >= 0 && y < room && i < len(vis) {
			md.pick = i
			return keyOf("enter"), true
		}
	}
	return tea.KeyPressMsg{}, false
}

// collapsePool hides the tickets in closed "up for grabs" lanes, except ones an agent
// is already on.
func collapsePool(rows []board.Row) []board.Row {
	var out []board.Row
	for i, r := range rows {
		if r.Kind == board.TicketRow && r.Issue.Pool && (i+1 >= len(rows) || rows[i+1].Kind != board.AgentRow) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// isPoolLane reports whether row r is an "up for grabs" lane heading.
func isPoolLane(r board.Row) bool { return r.Kind == board.StateRow && r.Text == board.PoolLane }

// togglePool opens or closes the "up for grabs" lanes, keeping the cursor on the heading.
func (m *Model) togglePool() {
	nth := 0 // which team's pool lane the cursor is on
	for _, r := range m.rows[:min(m.cursor+1, len(m.rows))] {
		if isPoolLane(r) {
			nth++
		}
	}
	m.poolOpen = !m.poolOpen
	m.rebuild()
	for i, r := range m.rows {
		if isPoolLane(r) {
			if nth--; nth == 0 {
				m.cursor = i
				return
			}
		}
	}
}

// dropAdopted leaves out sessions that a running lanes agent was forked from: adopting
// keeps the original running, but it's the same work, so the board shows it once.
func dropAdopted(agents []agent.Agent, live []state.Record) []agent.Agent {
	from := map[string]bool{}
	for _, r := range live {
		if r.AdoptedFrom != "" {
			from[r.AdoptedFrom] = true
		}
	}
	if len(from) == 0 {
		return agents
	}
	var out []agent.Agent
	for _, a := range agents {
		if a.RecordID == "" && from[a.ID] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// --- teams ---

// foldTeams hides everything under the headers of folded teams.
func foldTeams(rows []board.Row, folded map[string]bool) []board.Row {
	if len(folded) == 0 {
		return rows
	}
	var out []board.Row
	hide := false
	for _, r := range rows {
		switch r.Kind {
		case board.TeamRow:
			hide = folded[r.Text]
			out = append(out, r)
			continue
		case board.UnlinkedRow:
			hide = false
		}
		if !hide {
			out = append(out, r)
		}
	}
	return out
}

// teamWork counts each team's working and waiting agents, so a folded team's header
// can still show them.
func teamWork(rows []board.Row) map[string][2]int {
	out := map[string][2]int{}
	team := ""
	for _, r := range rows {
		switch {
		case r.Kind == board.TeamRow:
			team = r.Text
		case r.Kind == board.UnlinkedRow:
			team = ""
		case r.Kind == board.AgentRow && team != "" && !r.Agent.Ended:
			c := out[team]
			switch r.Agent.Status {
			case agent.Working:
				c[0]++
			case agent.Waiting:
				c[1]++
			}
			out[team] = c
		}
	}
	return out
}

// toggleTeam folds or unfolds a team, keeping the cursor on its header.
func (m *Model) toggleTeam(team string) {
	if m.folded == nil {
		m.folded = map[string]bool{}
	}
	m.folded[team] = !m.folded[team]
	m.rebuild()
	for i, r := range m.rows {
		if r.Kind == board.TeamRow && r.Text == team {
			m.cursor = i
			return
		}
	}
}

// jumpTeam moves to the next (or previous) team header.
func (m *Model) jumpTeam(next bool) tea.Cmd {
	step := -1
	if next {
		step = 1
	}
	for i := m.cursor + step; i >= 0 && i < len(m.rows); i += step {
		if m.rows[i].Kind == board.TeamRow {
			return m.moveTo(i)
		}
	}
	return nil
}

// rowIDs names each row by what it shows — a ticket by its key, an agent by its session,
// a heading by its place — so a selection can be found again after rows move.
func rowIDs(rows []board.Row) []string {
	ids := make([]string, len(rows))
	team, lane := "", ""
	for i, r := range rows {
		switch r.Kind {
		case board.TeamRow:
			team, lane = r.Text, ""
			ids[i] = "team:" + team
		case board.StateRow:
			lane = r.Text
			ids[i] = "lane:" + team + "/" + lane
		case board.ProjectRow:
			ids[i] = "project:" + team + "/" + lane + "/" + r.Text
		case board.UnlinkedRow:
			team, lane = "", ""
			ids[i] = "unlinked"
		case board.TicketRow:
			ids[i] = "ticket:" + r.Issue.Key
		case board.AgentRow:
			ids[i] = "agent:" + r.Agent.Tool + ":" + r.Agent.ID + ":" + r.Agent.RecordID
		}
	}
	return ids
}
