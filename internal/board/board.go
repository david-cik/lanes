// Package board turns issues and agents into the rows the UI draws.
package board

import (
	"cmp"
	"slices"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/link"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

type Kind int

const (
	TeamRow Kind = iota
	StateRow
	TicketRow
	AgentRow
	UnlinkedRow
)

type Row struct {
	Kind  Kind
	Level int // indent depth
	Text  string
	Issue *tracker.Issue
	Agent *agent.Agent
}

// PoolLane is the lane, last in each team, of unassigned tickets ready to pick up.
const PoolLane = "Up for grabs"

// stateRank orders a team's workflow states; Linear's MCP exposes no position.
var stateRank = map[string]int{"triage": 0, "backlog": 1, "unstarted": 2, "started": 3}

// Build groups issues team → state → ticket → agents. Agents whose ticket is not
// in the list go to a trailing Unlinked group. It sets TicketKey on the agents
// passed in, so callers should pass a copy they own.
//
// An agent's ticket is, in order: preset (launched by lanes), a manual link
// (links["tool:sessionID"]), or inferred from branch/name/path. stateOrder maps a
// team name to its workflow states in display order.
func Build(issues []tracker.Issue, agents []agent.Agent, links state.Links, stateOrder map[string][]string) []Row {
	pre := link.Prefixes(issues)
	byKey := map[string][]*agent.Agent{}
	var unlinked []*agent.Agent
	open := map[string]bool{}
	for _, i := range issues {
		open[i.Key] = true
	}
	for idx := range agents {
		a := &agents[idx]
		if a.TicketKey == "" {
			a.TicketKey = links[state.LinkKey(a.Tool, a.ID)]
		}
		if a.TicketKey == state.NoTicket { // removed from its ticket on purpose
			a.TicketKey = ""
			unlinked = append(unlinked, a)
			continue
		}
		if a.TicketKey == "" {
			a.TicketKey = link.Match(*a, pre)
		}
		if open[a.TicketKey] {
			byKey[a.TicketKey] = append(byKey[a.TicketKey], a)
		} else {
			unlinked = append(unlinked, a)
		}
	}

	sorted := slices.Clone(issues)
	slices.SortStableFunc(sorted, func(a, b tracker.Issue) int {
		return cmp.Or(
			cmp.Compare(a.Team, b.Team),
			poolLast(a, b),
			cmp.Compare(listed(stateOrder[a.Team], a.State), listed(stateOrder[b.Team], b.State)),
			cmp.Compare(rank(a.StateType), rank(b.StateType)),
			cmp.Compare(a.State, b.State),
			b.UpdatedAt.Compare(a.UpdatedAt), // most recently updated first
		)
	})

	var rows []Row
	for idx := range sorted {
		i := &sorted[idx]
		if idx == 0 || i.Team != sorted[idx-1].Team {
			rows = append(rows, Row{Kind: TeamRow, Text: i.Team})
		}
		if idx == 0 || i.Team != sorted[idx-1].Team || lane(*i) != lane(sorted[idx-1]) {
			rows = append(rows, Row{Kind: StateRow, Level: 1, Text: lane(*i)})
		}
		rows = append(rows, Row{Kind: TicketRow, Level: 2, Text: i.Key + " " + i.Title, Issue: i})
		for _, a := range byKey[i.Key] {
			rows = append(rows, Row{Kind: AgentRow, Level: 3, Agent: a})
		}
	}
	if len(unlinked) > 0 {
		rows = append(rows, Row{Kind: UnlinkedRow, Text: "Unlinked"})
		for _, a := range unlinked {
			rows = append(rows, Row{Kind: AgentRow, Level: 1, Agent: a})
		}
	}
	return rows
}

// lane is the board lane an issue sits in: its state, or PoolLane.
func lane(i tracker.Issue) string {
	if i.Pool {
		return PoolLane
	}
	return i.State
}

func poolLast(a, b tracker.Issue) int {
	switch {
	case a.Pool == b.Pool:
		return 0
	case a.Pool:
		return 1
	}
	return -1
}

// listed is a state's position in a configured order; unlisted states sort after.
func listed(order []string, state string) int {
	if i := slices.Index(order, state); i >= 0 {
		return i
	}
	return len(order)
}

func rank(stateType string) int {
	if r, ok := stateRank[stateType]; ok {
		return r
	}
	return len(stateRank)
}
