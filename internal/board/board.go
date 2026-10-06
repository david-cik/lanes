// Package board turns issues and agents into the rows the UI draws.
package board

import (
	"cmp"
	"slices"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/link"
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

// stateRank orders a team's workflow states; Linear's MCP exposes no position.
var stateRank = map[string]int{"triage": 0, "backlog": 1, "unstarted": 2, "started": 3}

// Build groups issues team → state → ticket → agents. Agents whose ticket is not
// in the list go to a trailing Unlinked group. It sets TicketKey on the agents
// passed in, so callers should pass a copy they own.
func Build(issues []tracker.Issue, agents []agent.Agent) []Row {
	pre := link.Prefixes(issues)
	byKey := map[string][]*agent.Agent{}
	var unlinked []*agent.Agent
	open := map[string]bool{}
	for _, i := range issues {
		open[i.Key] = true
	}
	for idx := range agents {
		a := &agents[idx]
		a.TicketKey = link.Match(*a, pre)
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
		if idx == 0 || i.Team != sorted[idx-1].Team || i.State != sorted[idx-1].State {
			rows = append(rows, Row{Kind: StateRow, Level: 1, Text: i.State})
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

func rank(stateType string) int {
	if r, ok := stateRank[stateType]; ok {
		return r
	}
	return len(stateRank)
}
