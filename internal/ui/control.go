package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/suggest"
	"github.com/david-cik/lanes/internal/tracker"
)

// Control requests arrive on the hook socket from `lanes agents|start|send|read|wait`,
// so another agent (a project's coordinator) can see and steer the board's agents.
// Starting an agent always goes through the same confirmation screen as s does.

// ctlEvent is something a coordinator waiting with `lanes wait` hears about.
type ctlEvent struct {
	Seq    int       `json:"seq"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`  // started, ready (finished a turn), needs_you, ended
	Agent  string    `json:"agent"` // the agent's id (as `lanes agents` lists it)
	Ticket string    `json:"ticket,omitempty"`
	Text   string    `json:"text,omitempty"`
}

const ctlLogMax = 200

type waiter struct {
	reply func([]byte)
	after int
	only  []string // agent ids or ticket keys; empty = any
}

type ctlTimeoutMsg struct{ id int }

// ctlAgent is one agent as control requests list it.
type ctlAgent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Tool     string    `json:"tool"`
	Status   string    `json:"status"`
	Since    time.Time `json:"since,omitzero"`
	Ready    bool      `json:"ready,omitempty"`   // finished a turn nobody has looked at yet
	Waiting  string    `json:"waiting,omitempty"` // what it needs from the user
	Lanes    bool      `json:"lanes"`             // started by lanes: send works on it
	Cwd      string    `json:"cwd,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Recent   []string  `json:"recent,omitempty"` // latest activity, newest last
	Ticket   string    `json:"ticket,omitempty"`
	ended    bool
	session  string
	pane     string
	recordID string
}

type ctlTicket struct {
	Key     string     `json:"key"`
	Title   string     `json:"title"`
	State   string     `json:"state"`
	Team    string     `json:"team"`
	Project string     `json:"project,omitempty"`
	Pool    bool       `json:"up_for_grabs,omitempty"`
	URL     string     `json:"url,omitempty"`
	Agents  []ctlAgent `json:"agents"`
}

func ctlReply(ev hook.Event, v any) {
	b, _ := json.Marshal(v)
	reply(ev, b)
}

func ctlError(ev hook.Event, err error) { ctlReply(ev, map[string]string{"error": err.Error()}) }

func (m *Model) control(ev hook.Event) tea.Cmd {
	if ev.Closed {
		delete(m.waiters, ev.ID)
		return nil
	}
	var args struct {
		Project string   `json:"project"`
		Ticket  string   `json:"ticket"`
		Agent   string   `json:"agent"`
		Text    string   `json:"text"`
		After   *int     `json:"after"`
		Only    []string `json:"only"`
		Timeout float64  `json:"timeout"` // seconds
	}
	if len(ev.Payload) > 0 && json.Unmarshal(ev.Payload, &args) != nil {
		ctlError(ev, fmt.Errorf("bad arguments"))
		return nil
	}
	switch ev.Control {
	case "agents":
		tickets, unlinked := m.ctlBoard(args.Project)
		ctlReply(ev, map[string]any{"tickets": tickets, "unlinked": unlinked, "seq": m.ctlSeq})
	case "start":
		return m.ctlStart(ev, args.Ticket, args.Text)
	case "send":
		a, err := m.ctlFind(args.Agent)
		if err == nil && (a.recordID == "" || a.pane == "") {
			err = fmt.Errorf("%s was not started by lanes, so it can't be sent to", a.Name)
		}
		if err == nil && (m.stopping[a.recordID] || a.ended) {
			err = fmt.Errorf("%s has stopped", a.Name)
		}
		if err == nil && m.opt.Tmux == nil {
			err = fmt.Errorf("lanes isn't running inside tmux")
		}
		if err == nil && strings.TrimSpace(args.Text) == "" {
			err = fmt.Errorf("nothing to send")
		}
		if err != nil {
			ctlError(ev, err)
			return nil
		}
		if st := m.hooks[a.ID]; st != nil {
			st.log(m.now(), "sent by another agent: "+strings.Join(strings.Fields(args.Text), " "))
		}
		tm, pane, text := m.opt.Tmux, a.pane, args.Text
		return func() tea.Msg {
			if err := tm.SendText(pane, text); err != nil {
				ctlError(ev, err)
			} else {
				ctlReply(ev, map[string]any{"sent": true, "agent": a.ID})
			}
			return nil
		}
	case "read":
		a, err := m.ctlFind(args.Agent)
		if err != nil {
			ctlError(ev, err)
			return nil
		}
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		return func() tea.Msg {
			p := suggest.TranscriptPath(dir, a.Cwd, a.session)
			if p == "" {
				ctlError(ev, fmt.Errorf("%s has no transcript yet", a.Name))
				return nil
			}
			ctlReply(ev, map[string]any{"agent": a.ID, "ticket": a.Ticket, "status": a.Status, "reply": suggest.LastReply(p)})
			return nil
		}
	case "wait":
		w := waiter{reply: ev.Reply, after: m.ctlSeq, only: args.Only}
		if args.After != nil {
			w.after = *args.After
		}
		if m.answerWaiter(w) {
			return nil
		}
		m.waiters[ev.ID] = w
		d := time.Duration(args.Timeout * float64(time.Second))
		if d <= 0 {
			d = 90 * time.Second
		}
		id := ev.ID
		return tea.Tick(d, func(time.Time) tea.Msg { return ctlTimeoutMsg{id} })
	default:
		ctlError(ev, fmt.Errorf("unknown request %q", ev.Control))
	}
	return nil
}

func (m *Model) ctlTimeout(id int) {
	if w, ok := m.waiters[id]; ok {
		delete(m.waiters, id)
		b, _ := json.Marshal(map[string]any{"events": []ctlEvent{}, "seq": max(w.after, m.ctlSeq), "timeout": true})
		w.reply(b)
	}
}

// answerWaiter sends w the events it is waiting for, if any have happened.
func (m *Model) answerWaiter(w waiter) bool {
	var got []ctlEvent
	for _, e := range m.ctlLog {
		if e.Seq > w.after && (len(w.only) == 0 || slices.ContainsFunc(w.only, func(s string) bool {
			return strings.EqualFold(s, e.Agent) || strings.EqualFold(s, e.Ticket)
		})) {
			got = append(got, e)
		}
	}
	if len(got) == 0 {
		return false
	}
	b, _ := json.Marshal(map[string]any{"events": got, "seq": got[len(got)-1].Seq})
	w.reply(b)
	return true
}

// event records something a coordinator may be waiting on and wakes its waiters.
func (m *Model) event(kind, id, text string) {
	m.ctlSeq++
	e := ctlEvent{Seq: m.ctlSeq, At: m.now(), Kind: kind, Agent: id, Text: text}
	if i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == id }); i >= 0 && m.live[i].TicketKey != state.NoTicket {
		e.Ticket = m.live[i].TicketKey
	} else if c, err := m.ctlFind(id); err == nil {
		e.Ticket = c.Ticket
	}
	m.ctlLog = append(m.ctlLog, e)
	if n := len(m.ctlLog); n > ctlLogMax {
		m.ctlLog = m.ctlLog[n-ctlLogMax:]
	}
	for wid, w := range m.waiters {
		if m.answerWaiter(w) {
			delete(m.waiters, wid)
		}
	}
}

// ctlBoard is the board's tickets (of one project, if given) with their agents, and
// the agents on no ticket.
func (m *Model) ctlBoard(project string) ([]ctlTicket, []ctlAgent) {
	agents := slices.Clone(m.agents)
	m.overlay(agents)
	board.Build(m.issues, agents, m.links, m.stateOrder()) // fills in each agent's ticket
	byKey := map[string][]ctlAgent{}
	var unlinked []ctlAgent
	open := map[string]bool{}
	for _, i := range m.issues {
		open[i.Key] = true
	}
	for i := range agents {
		c := m.ctlAgentOf(&agents[i])
		if open[c.Ticket] {
			byKey[c.Ticket] = append(byKey[c.Ticket], c)
		} else {
			unlinked = append(unlinked, c)
		}
	}
	tickets := []ctlTicket{}
	for _, i := range m.issues {
		if project != "" && !strings.EqualFold(i.Project, project) {
			continue
		}
		t := ctlTicket{Key: i.Key, Title: i.Title, State: i.State, Team: i.Team, Project: i.Project, Pool: i.Pool, URL: i.URL,
			Agents: byKey[i.Key]}
		if t.Agents == nil {
			t.Agents = []ctlAgent{}
		}
		tickets = append(tickets, t)
	}
	if project != "" || unlinked == nil {
		unlinked = []ctlAgent{}
	}
	return tickets, unlinked
}

func (m *Model) ctlAgentOf(a *agent.Agent) ctlAgent {
	c := ctlAgent{ID: hookKey(a), Name: a.Name, Tool: a.Tool, Status: string(a.Status), Since: a.Since, Lanes: a.RecordID != "",
		Cwd: a.Cwd, Branch: a.Branch, Ticket: a.TicketKey, ended: a.Ended, session: a.ID, pane: a.Pane, recordID: a.RecordID}
	if a.Ended {
		c.Status = "ended"
	}
	if st := m.hooks[c.ID]; st != nil {
		c.Ready = st.ready
		c.Waiting = strings.TrimPrefix(m.waitingNote(a), " · ")
		for _, e := range st.activity[max(0, len(st.activity)-5):] {
			c.Recent = append(c.Recent, e.at.Format("15:04")+" "+e.text)
		}
	}
	return c
}

// ctlFind finds one agent by id, id prefix, ticket key (when one agent is on it), or name.
func (m *Model) ctlFind(ref string) (ctlAgent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ctlAgent{}, fmt.Errorf("which agent? give its id or ticket (lanes agents lists them)")
	}
	tickets, unlinked := m.ctlBoard("")
	all := unlinked
	for _, t := range tickets {
		all = append(all, t.Agents...)
	}
	for _, match := range []func(c ctlAgent) bool{
		func(c ctlAgent) bool { return c.ID == ref || c.session == ref },
		func(c ctlAgent) bool {
			return len(ref) >= 4 && (strings.HasPrefix(c.ID, ref) || strings.HasPrefix(c.session, ref))
		},
		func(c ctlAgent) bool { return strings.EqualFold(c.Ticket, ref) && !c.ended },
		func(c ctlAgent) bool { return c.Name == ref },
	} {
		var hits []ctlAgent
		for _, c := range all {
			if match(c) {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		}
		var ids []string
		for _, c := range hits {
			ids = append(ids, c.ID)
		}
		return ctlAgent{}, fmt.Errorf("%q matches %d agents (%s); use an id", ref, len(hits), strings.Join(ids, ", "))
	}
	return ctlAgent{}, fmt.Errorf("no agent matches %q (lanes agents lists them)", ref)
}

// ctlStart asks the user, in the panel, to start an agent on a ticket. It answers right
// away; a "started" event follows if the user says yes.
func (m *Model) ctlStart(ev hook.Event, key, instructions string) tea.Cmd {
	i := slices.IndexFunc(m.issues, func(i tracker.Issue) bool { return strings.EqualFold(i.Key, strings.TrimSpace(key)) })
	switch {
	case i < 0:
		ctlError(ev, fmt.Errorf("%s isn't on the board (lanes agents lists the tickets)", key))
		return nil
	case m.opt.Tmux == nil:
		ctlError(ev, fmt.Errorf("lanes isn't running inside tmux"))
		return nil
	case m.modal != nil || m.review != nil:
		ctlError(ev, fmt.Errorf("the panel is showing the user something else; try again in a minute"))
		return nil
	}
	issue := m.issues[i]
	m.startNote[issue.Key] = strings.TrimSpace(instructions)
	cmd := m.startOn(issue)
	ctlReply(ev, map[string]any{"asked": true, "ticket": issue.Key,
		"next": "the user confirms in the lanes panel; `lanes wait` reports a \"started\" event with the new agent's id"})
	return tea.Batch(cmd, m.ping("an agent asks to start work on "+issue.Key+" — confirm in lanes"))
}
