package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/tracker"
)

type createdMsg struct {
	key string
	err error
}

// newTicket (N) creates a ticket assigned to you, in the team (and project) of the
// row you're on: a title, then an optional description.
func (m *Model) newTicket() tea.Cmd {
	cr, ok := m.opt.Tracker.(tracker.Creator)
	if !ok {
		m.say("", fmt.Errorf("this tracker can't create tickets"))
		return nil
	}
	team, project := m.selectedTeam()
	if team == "" {
		var teams []choice
		for _, i := range m.issues {
			if !slices.ContainsFunc(teams, func(c choice) bool { return c.value == i.Team }) {
				teams = append(teams, choice{i.Team, i.Team})
			}
		}
		if len(teams) == 0 {
			m.say("", fmt.Errorf("no team to create a ticket in yet"))
			return nil
		}
		m.modal = &modal{title: "New ticket in which team?", items: teams,
			onChoose: func(c choice) tea.Cmd { m.askTitle(cr, c.value, ""); return nil }}
		return nil
	}
	m.askTitle(cr, team, project)
	return nil
}

// selectedTeam is the team (and project) of the row under the cursor, or of the only
// team on the board.
func (m *Model) selectedTeam() (team, project string) {
	if i, ok := m.selectedIssue(); ok {
		return i.Team, i.Project
	}
	for j := min(m.cursor, len(m.rows)-1); j >= 0; j-- { // a heading: the team it's under
		if m.rows[j].Kind == board.UnlinkedRow {
			break // sessions on no ticket belong to no team
		}
		if m.rows[j].Kind == board.TeamRow {
			return m.rows[j].Text, ""
		}
	}
	var teams []string
	for _, i := range m.issues {
		if !slices.Contains(teams, i.Team) {
			teams = append(teams, i.Team)
		}
	}
	if len(teams) == 1 {
		return teams[0], ""
	}
	return "", ""
}

func (m *Model) askTitle(cr tracker.Creator, team, project string) {
	var lines []string
	if project != "" {
		lines = append(lines, faint.Render("in project "+project+" (from the ticket you're on) · assigned to you · Todo"))
	} else {
		lines = append(lines, faint.Render("assigned to you · Todo"))
	}
	m.modal = &modal{input: true, title: "New " + team + " ticket — title", lines: lines,
		onInput: func(title string) tea.Cmd {
			title = strings.TrimSpace(title)
			if title == "" {
				return nil
			}
			m.modal = &modal{input: true, title: "Description for \"" + title + "\" (optional — enter skips)",
				hint: "enter create · esc cancel",
				onInput: func(desc string) tea.Cmd {
					m.say("creating "+team+" ticket…", nil)
					return func() tea.Msg {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						key, err := cr.Create(ctx, team, title, strings.TrimSpace(desc), project)
						return createdMsg{key, err}
					}
				}}
			return nil
		}}
}

func (m *Model) created(msg createdMsg) tea.Cmd {
	switch {
	case errors.Is(msg.err, tracker.ErrNoKey): // made, but the answer didn't say as what
		m.say("created the ticket (refreshing to find it)", nil)
		return m.fetchIssues()
	case msg.err != nil:
		m.say("", fmt.Errorf("couldn't create the ticket: %w", msg.err))
		return nil
	}
	m.follow = msg.key
	m.say("created "+msg.key+" — n starts an agent on it", nil)
	return m.fetchIssues()
}

// followNew moves the cursor to a just-created ticket on the first refresh after it
// was made — if it's on screen then (a filter or a folded team may hide it), not later.
func (m *Model) followNew() {
	for i, r := range m.rows {
		if m.follow != "" && r.Kind == board.TicketRow && r.Issue.Key == m.follow {
			m.cursor = i
			break
		}
	}
	m.follow = ""
}

// siblings tells an agent about the other open tickets in its ticket's project that
// lanes knows of — yours and the unassigned ones up for grabs — and which have an agent
// on them, so it stays in its lane instead of doing their work.
// It returns "" when the ticket has no project or the project no other tickets.
func (m *Model) siblings(key string) (string, int) {
	var self tracker.Issue
	for _, i := range m.issues {
		if i.Key == key {
			self = i
		}
	}
	if self.Project == "" {
		return "", 0
	}
	busy := map[string]bool{}
	for _, a := range m.agents {
		if !a.Ended {
			busy[a.TicketKey] = true
		}
	}
	var lines []string
	for _, i := range m.issues {
		if i.Project != self.Project || i.Key == key {
			continue
		}
		note := i.State
		if busy[i.Key] {
			note += ", an agent is working on it"
		}
		lines = append(lines, fmt.Sprintf("- %s %s (%s)", i.Key, strings.Join(strings.Fields(i.Title), " "), note))
	}
	if len(lines) == 0 {
		return "", 0
	}
	return fmt.Sprintf("This ticket is part of the project %q, which also has these open tickets (yours, and unassigned ones):\n%s\n\n"+
		"Stay within %s. Work that belongs to one of those tickets is done there, by whoever works on it; "+
		"if this ticket needs it, say so instead of doing it yourself.",
		self.Project, strings.Join(lines, "\n"), key), len(lines)
}
