package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/sessions"
	"github.com/david-cik/lanes/internal/suggest"
	"github.com/david-cik/lanes/internal/tracker"
)

type createdMsg struct {
	key     string
	err     error
	session string // a session to put on the new ticket ("" = none)
	tool    string // its tool
	name    string // its name, for the message
}

// newTicket (N) creates a ticket assigned to you, in the team (and project) of the
// row you're on. Claude drafts it — from a few words you type, or, on a session's row,
// from that conversation (and the session goes on the new ticket) — and you review it.
func (m *Model) newTicket() tea.Cmd {
	cr, ok := m.opt.Tracker.(tracker.Creator)
	if !ok {
		m.say("", fmt.Errorf("this tracker can't create tickets"))
		return nil
	}
	var from *agent.Agent // the session to draft from, if the cursor is on one
	if r, ok := m.selected(); ok && r.Kind == board.AgentRow && m.transcripts(r.Agent.Tool) != nil {
		from = r.Agent
	}
	team, project := m.selectedTeam()
	if team != "" {
		return m.startDraft(cr, team, project, from)
	}
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
		onChoose: func(c choice) tea.Cmd { return m.startDraft(cr, c.value, "", from) }}
	return nil
}

// startDraft asks for a few words, or drafts straight from the session.
func (m *Model) startDraft(cr tracker.Creator, team, project string, from *agent.Agent) tea.Cmd {
	t := ticketDraft{cr: cr, team: team, project: project, from: from}
	if from != nil {
		t.session, t.tool, t.name = from.ID, from.Tool, from.Name
		m.say(m.askName()+" is drafting a ticket from "+from.Name+"…", nil)
		return m.draft(t)
	}
	where := "assigned to you · Todo"
	if project != "" {
		where = "in project " + project + " · " + where
	}
	m.modal = &modal{input: true, title: "New " + team + " ticket — what's it about? (" + m.askName() + " drafts it)",
		lines: []string{faint.Render(where)},
		onInput: func(words string) tea.Cmd {
			if strings.TrimSpace(words) == "" {
				return nil
			}
			t.words = words
			m.say(m.askName()+" is drafting the ticket…", nil)
			return m.draft(t)
		}}
	return nil
}

// ticketDraft is a ticket on its way to being created.
type ticketDraft struct {
	cr                   tracker.Creator
	team, project        string
	title, description   string
	session, tool, name  string // drafted from this session, which goes on the ticket
	words                string // what you typed, when not drafted from a session
	from                 *agent.Agent
	anyway               bool   // you chose to create it although Claude saw a duplicate
	duplicate, dupReason string // Claude thinks this open ticket already covers it
	err                  error
}

type draftMsg struct{ ticketDraft }

const draftSystem = `You draft a ticket for someone's issue tracker. You get what they want (their
own words, or a coding session's title and prompts), the team and project, and their
open tickets. Reply with only one JSON object, no prose:
{"title":"<imperative, at most 70 characters>","description":"<markdown: why, what to do, done when>"}
If one of the open tickets already covers this (and "create_anyway" isn't true), reply instead:
{"duplicate":"<its key>","reason":"<one short sentence>"}`

// draft has Claude write the ticket (off the UI loop), from t.words or t.from.
func (m *Model) draft(t ticketDraft) tea.Cmd {
	words, from := t.words, t.from
	type ticket struct{ Key, Title, State string }
	var open []ticket
	for _, i := range m.issues {
		open = append(open, ticket{i.Key, i.Title, i.State})
	}
	in := map[string]any{"team": t.team, "project": t.project, "open_tickets": open, "create_anyway": t.anyway}
	if words != "" {
		in["request"] = words
	}
	var path, cwd string
	var tr agent.Transcripts
	if from != nil {
		cwd, tr = from.Cwd, m.transcripts(from.Tool)
	}
	if tr != nil {
		path = tr.TranscriptPath(cwd, from.ID)
	}
	run := m.readers.Ask
	return func() tea.Msg {
		if path != "" {
			info, _ := tr.SessionInfo(path)
			in["session"] = map[string]any{"title": info.Title, "folder": cwd,
				"first_prompt": info.First, "recent_prompts": suggest.Prompts(tr, path, 8)}
		}
		data, _ := json.Marshal(in)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := run(ctx, draftSystem, string(data))
		if err != nil {
			t.err = err
			return draftMsg{t}
		}
		var r struct{ Title, Description, Duplicate, Reason string }
		if json.Unmarshal([]byte(jsonObj.FindString(out)), &r) != nil || (r.Title == "" && r.Duplicate == "") {
			t.err = fmt.Errorf("the draft wasn't understood: %s", firstLine(out))
			return draftMsg{t}
		}
		t.title, t.description = strings.Join(strings.Fields(r.Title), " "), strings.TrimSpace(r.Description)
		t.duplicate, t.dupReason = r.Duplicate, r.Reason
		return draftMsg{t}
	}
}

func (m *Model) gotDraft(t ticketDraft) tea.Cmd {
	m.notice = ""
	switch {
	case t.err != nil: // no draft: type it yourself
		m.say("", fmt.Errorf("no draft (%v) — type the ticket yourself", t.err))
		m.askTitle(t.cr, t.team, t.project)
		return nil
	case t.duplicate != "" && slices.ContainsFunc(m.issues, func(i tracker.Issue) bool { return i.Key == t.duplicate }):
		acts := []action{{"c", "create a new ticket anyway", func() (tea.Cmd, bool) {
			t.duplicate, t.anyway = "", true // same words or session, no duplicate check
			m.say(m.askName()+" is drafting the ticket…", nil)
			return m.draft(t), false
		}}}
		if t.session != "" {
			acts = append([]action{{"l", "put " + t.name + " on " + t.duplicate + " instead", func() (tea.Cmd, bool) {
				err := sessions.Link(context.Background(), m.opt.Store, t.tool, t.session, t.duplicate)
				m.links, _ = m.opt.Store.Links()
				m.rebuild()
				m.say("put "+t.name+" on "+t.duplicate, err)
				return m.fetchAgents(), false
			}}}, acts...)
		}
		m.modal = &modal{title: t.duplicate + " may already cover this", lines: []string{clean(t.dupReason), ""}, actions: acts}
		return nil
	case t.title == "": // a "duplicate" that isn't on the board, and no draft
		m.say("", fmt.Errorf("no ticket was drafted — type it yourself"))
		m.askTitle(t.cr, t.team, t.project)
		return nil
	}
	m.reviewDraft(t)
	return nil
}

// reviewDraft shows the draft: create it, edit it in $EDITOR, or drop it.
func (m *Model) reviewDraft(t ticketDraft) {
	where := t.team + " · Todo · assigned to you"
	if t.project != "" {
		where = t.team + " · " + t.project + " · Todo · assigned to you"
	}
	lines := []string{faint.Render(where)}
	if t.session != "" {
		lines = append(lines, faint.Render("then "+t.name+" goes on it"))
	}
	lines = append(lines, "", bold.Render(clean(t.title)), "")
	for _, l := range strings.Split(t.description, "\n") {
		lines = append(lines, clean(l))
	}
	lines = append(lines, "")
	create := func() (tea.Cmd, bool) {
		m.say("creating "+t.team+" ticket…", nil)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			key, err := t.cr.Create(ctx, t.team, t.title, t.description, t.project)
			return createdMsg{key: key, err: err, session: t.session, tool: t.tool, name: t.name}
		}, false
	}
	m.modal = &modal{title: "New ticket", lines: lines, hint: "↑↓ scroll · esc drop it", actions: []action{
		{"y", "create it", create}, {"enter", "", create},
		{"e", "edit it in $EDITOR first", func() (tea.Cmd, bool) { return editDraft(t), false }},
	}}
}

type editedMsg struct {
	ticketDraft
	err error
}

// editDraft opens the draft in $EDITOR (first line: the title; the rest: description).
func editDraft(t ticketDraft) tea.Cmd {
	f, err := os.CreateTemp("", "lanes-ticket-*.md")
	if err != nil {
		return func() tea.Msg { return editedMsg{t, err} }
	}
	f.WriteString(t.title + "\n\n" + t.description + "\n")
	f.Close()
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	args := append(strings.Fields(editor), f.Name())
	return tea.ExecProcess(exec.Command(args[0], args[1:]...), func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editedMsg{t, err}
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			return editedMsg{t, err}
		}
		title, rest, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
		t.title, t.description = strings.TrimSpace(strings.TrimPrefix(title, "# ")), strings.TrimSpace(rest)
		return editedMsg{t, nil}
	})
}

func (m *Model) edited(msg editedMsg) tea.Cmd {
	if msg.err != nil {
		m.say("", fmt.Errorf("editing the draft: %w", msg.err))
	}
	if msg.title == "" {
		m.say("", fmt.Errorf("the draft has no title now; dropped it"))
		return nil
	}
	m.reviewDraft(msg.ticketDraft)
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
						return createdMsg{key: key, err: err}
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
	if msg.session == "" {
		m.say("created "+msg.key+" — "+m.k("start")+" starts an agent on it", nil)
		return m.fetchIssues()
	}
	err := sessions.Link(context.Background(), m.opt.Store, msg.tool, msg.session, msg.key)
	m.links, _ = m.opt.Store.Links()
	m.say("created "+msg.key+" and put "+msg.name+" on it", err)
	return tea.Batch(m.fetchIssues(), m.fetchAgents())
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
