package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/launch"
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

// startNote is what an agent asked for with `lanes start`. It only applies to a start
// screen that follows soon, so an abandoned request can't ride along on a later s.
type startNote struct {
	text string
	at   time.Time
}

const startNoteTTL = 2 * time.Minute

type waiter struct {
	reply func([]byte)
	after int
	only  []string // agent ids or ticket keys; empty = any
	self  string   // the waiting agent's own id: its own events aren't news to it
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
	Project  string    `json:"coordinates,omitempty"` // the project, for a project's coordinator
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
		Self    string   `json:"self"`    // the caller's own agent id (LANES_AGENT_ID), if it's an agent
		Request string   `json:"request"` // approve/deny: the request being answered, as agents/wait show it
		Message string   `json:"message"` // deny: why, for the agent
		Repo    string   `json:"repo"`    // start: the repo to work in (name or path)
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
		if m.isCoordinator(args.Self) && m.opt.Config.Coordinator.Start != "panel" {
			return m.startDirect(ev, args.Ticket, args.Text, args.Repo)
		}
		return m.ctlStart(ev, args.Ticket, args.Text)
	case "approve", "deny":
		if !m.isCoordinator(args.Self) { // what was only yours to answer goes no further
			ctlError(ev, fmt.Errorf("only a project's coordinator can answer agents' prompts; the user answers them in the lanes panel"))
			return nil
		}
		if err := m.inProject(args.Self, args.Agent); err != nil {
			ctlError(ev, err)
			return nil
		}
		return m.ctlAnswer(ev, args.Agent, args.Request, args.Message, ev.Control == "approve")
	case "stop":
		if err := m.inProject(args.Self, args.Agent); err != nil {
			ctlError(ev, err)
			return nil
		}
		return m.ctlStop(ev, args.Agent)
	case "send":
		a, err := m.ctlFind(args.Agent)
		if err == nil && (a.recordID == "" || a.pane == "") {
			err = fmt.Errorf("%s was not started by lanes, so it can't be sent to", a.Name)
		}
		if err == nil && (m.stopping[a.recordID] || a.ended) {
			err = fmt.Errorf("%s has stopped", a.Name)
		}
		// A dialog on screen (permission, question, folder trust) would take the text and
		// Enter as its answer. ponytail: status comes from hooks, so a dialog that opened in
		// the last moment can still slip past; a pane-capture check would close that.
		if st := m.hooks[a.ID]; err == nil && (a.Status == string(agent.Waiting) || st != nil && (st.pending != nil || st.trust)) {
			err = fmt.Errorf("%s is waiting on a prompt only the user can answer; tell the user", a.Name)
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
		tr := m.transcripts(a.Tool)
		if tr == nil {
			ctlError(ev, fmt.Errorf("lanes can't read %s's transcripts, so not what %s wrote", a.Tool, a.Name))
			return nil
		}
		return func() tea.Msg {
			p := tr.TranscriptPath(a.Cwd, a.session)
			if p == "" {
				ctlError(ev, fmt.Errorf("%s has no transcript yet", a.Name))
				return nil
			}
			ctlReply(ev, map[string]any{"agent": a.ID, "ticket": a.Ticket, "status": a.Status, "reply": suggest.LastReply(tr, p)})
			return nil
		}
	case "wait":
		w := waiter{reply: ev.Reply, after: m.ctlSeq, only: args.Only, self: args.Self}
		if args.After != nil && *args.After <= m.ctlSeq {
			w.after = *args.After
		} else if args.After != nil { // a seq this panel never gave out: everything it has is new
			w.after = 0
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
		if e.Seq > w.after && e.Agent != w.self && (len(w.only) == 0 || slices.ContainsFunc(w.only, func(s string) bool {
			return strings.EqualFold(s, e.Agent) || strings.EqualFold(s, e.Ticket)
		})) {
			got = append(got, e)
		}
	}
	if len(got) == 0 {
		return false
	}
	ans := map[string]any{"events": got, "seq": got[len(got)-1].Seq}
	if w.after < m.ctlLog[0].Seq-1 {
		ans["missed"] = true // older events than these were already dropped
	}
	b, _ := json.Marshal(ans)
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
		Cwd: a.Cwd, Branch: a.Branch, Ticket: a.TicketKey, Project: a.Project, ended: a.Ended, session: a.ID, pane: a.Pane, recordID: a.RecordID}
	if a.Ended {
		c.Status = "ended"
	}
	if st := m.hooks[c.ID]; st != nil {
		c.Ready = st.ready
		c.Waiting = requestText(st)
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
	m.startNote[issue.Key] = startNote{strings.TrimSpace(instructions), m.now()}
	cmd := m.startOn(issue)
	ctlReply(ev, map[string]any{"asked": true, "ticket": issue.Key,
		"next": "the user confirms in the lanes panel; `lanes wait` reports a \"started\" event with the new agent's id"})
	return tea.Batch(cmd, m.ping("an agent asks to start work on "+issue.Key+" — confirm in lanes"))
}

// requestText is what an agent is waiting on, as approve and deny expect it repeated.
func requestText(st *hookState) string {
	switch {
	case st.pending != nil:
		return strings.Join(strings.Fields(st.pending.req.Tool+": "+st.pending.req.Summary), " ")
	case st.trust:
		return trustRequest
	}
	return ""
}

const trustRequest = "folder-trust prompt"

// ctlAnswer allows or denies what an agent is waiting on. request must repeat it (as
// agents or wait showed it), so the caller's own permission prompt shows what is being
// answered and an answer can't land on a request that replaced it.
func (m *Model) ctlAnswer(ev hook.Event, ref, request, message string, allow bool) tea.Cmd {
	if m.opt.Config.Coordinator.Approve == "off" {
		ctlError(ev, fmt.Errorf("answering agents' prompts through lanes is off (coordinator.approve = \"off\"); the user answers them in the panel"))
		return nil
	}
	a, err := m.ctlFind(ref)
	if err != nil {
		ctlError(ev, err)
		return nil
	}
	st := m.hooks[a.ID]
	want := ""
	if st != nil {
		want = requestText(st)
	}
	switch {
	case want == "":
		ctlError(ev, fmt.Errorf("%s isn't waiting on anything", a.Name))
		return nil
	case strings.Join(strings.Fields(request), " ") != want:
		ctlError(ev, fmt.Errorf("%s is waiting on %q; pass that exactly as request", a.Name, want))
		return nil
	}
	if st.pending != nil {
		d := agent.Decision{Behavior: "deny", Message: message}
		if allow {
			d = agent.Decision{Behavior: "allow"}
		}
		m.decide(a.ID, st.pending, d)
		st.log(m.now(), "answered through lanes by another agent")
		ctlReply(ev, map[string]any{"answered": d.Behavior, "agent": a.ID})
		return nil
	}
	// The folder-trust prompt, answered with the keys its tool says mean yes.
	if !allow {
		ctlError(ev, fmt.Errorf("to refuse the folder, stop the agent"))
		return nil
	}
	if a.pane == "" || m.opt.Tmux == nil {
		ctlError(ev, fmt.Errorf("%s's pane isn't reachable", a.Name))
		return nil
	}
	tm, pane := m.opt.Tmux, a.pane
	t, _ := m.adapter(a.Tool).(agent.Truster)
	return func() tea.Msg {
		screen, err := tm.Capture(pane)
		var keys []string
		ok := false
		if err == nil && t != nil {
			keys, ok = t.TrustKeys(screen)
		}
		if err == nil && !ok {
			err = fmt.Errorf("the folder-trust prompt on screen isn't one lanes knows how to answer; answer it in the pane")
		}
		if err == nil {
			err = tm.SendKeys(pane, keys...)
		}
		if err != nil {
			ctlError(ev, err)
		} else {
			ctlReply(ev, map[string]any{"answered": "trusted", "agent": a.ID})
		}
		return nil
	}
}

// ctlStop stops an agent lanes started, keeping its worktree.
func (m *Model) ctlStop(ev hook.Event, ref string) tea.Cmd {
	a, err := m.ctlFind(ref)
	if err != nil {
		ctlError(ev, err)
		return nil
	}
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == a.recordID })
	switch {
	case i < 0 || m.opt.Tmux == nil:
		ctlError(ev, fmt.Errorf("%s was not started by lanes, so it can't be stopped from here", a.Name))
		return nil
	case m.stopping[a.recordID]:
		ctlError(ev, fmt.Errorf("%s is already stopping", a.Name))
		return nil
	}
	rec, ad, tm, store := m.live[i], m.adapter(a.Tool), m.opt.Tmux, m.opt.Store
	if m.shown == rec.ID {
		if err := m.unshow(); err != nil {
			m.say("", err)
		}
	}
	m.stopping[rec.ID] = true
	who := m.whoIs(rec.ID)
	return func() tea.Msg {
		err := stop(rec, ad, tm, store)
		if err != nil {
			ctlError(ev, err)
		} else {
			ctlReply(ev, map[string]any{"stopped": true, "agent": rec.ID})
		}
		return noticeMsg{"another agent stopped " + rec.Tool + " on " + who, err}
	}
}

// startDirect starts an agent for a coordinator without the panel's start screen (its
// own permission prompt asked the user, unless coordinator.start is "auto"): in the
// launch folder if one is set, else a new worktree in the ticket's repo (or repo).
func (m *Model) startDirect(ev hook.Event, key, instructions, repo string) tea.Cmd {
	i := slices.IndexFunc(m.issues, func(i tracker.Issue) bool { return strings.EqualFold(i.Key, strings.TrimSpace(key)) })
	ad := m.adapter(m.opt.Config.DefaultAgent)
	switch {
	case i < 0:
		ctlError(ev, fmt.Errorf("%s isn't on the board (agents lists the tickets)", key))
		return nil
	case m.opt.Tmux == nil:
		ctlError(ev, fmt.Errorf("lanes isn't running inside tmux"))
		return nil
	case ad == nil:
		ctlError(ev, fmt.Errorf("default_agent %q isn't set up", m.opt.Config.DefaultAgent))
		return nil
	}
	issue, cfg := m.issues[i], m.opt.Config
	sib, _ := m.siblings(issue.Key)
	live, tr, tm, store := slices.Clone(m.live), m.opt.Tracker, m.opt.Tmux, m.opt.Store
	hookBin, socket := "", ""
	if _, ok := ad.(agent.Hooker); ok {
		hookBin, socket = m.opt.HookBin, m.opt.Socket
	}
	var claim tea.Cmd
	if claimer, ok := tr.(tracker.Claimer); ok && issue.Pool {
		team, order := issue.Team, m.stateOrder()[issue.Team]
		claim = func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			st, err := claimer.Claim(ctx, issue.Key, team, order)
			return claimedMsg{key: issue.Key, state: st, err: err}
		}
	}
	m.say("another agent is starting "+ad.Name()+" on "+issue.Key+"…", nil)
	return func() tea.Msg {
		fail := func(err error) tea.Msg { return launchedMsg{err: err, ctl: ev} }
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		d, err := tr.Issue(ctx, issue.Key)
		if err != nil {
			return fail(err)
		}
		repos := launch.Repos(cfg.RepoRoots)
		var p launch.Plan
		switch {
		case repo != "":
			j := slices.IndexFunc(repos, func(r launch.Repo) bool { return r.Name == repo || r.Path == config.Expand(repo) })
			if j < 0 {
				return fail(fmt.Errorf("no repo %q under repo_roots; repos: %s", repo, repoNames(repos)))
			}
			p = launch.NewPlan(d, repos[j], ad, cfg)
		case cfg.LaunchDir != "":
			p = launch.FolderPlan(ctx, d, cfg.LaunchDir, ad, cfg)
		default:
			r, ok := launch.InferRepo(d, repos)
			if !ok {
				return fail(fmt.Errorf("which repo is %s in? pass repo, one of: %s", issue.Key, repoNames(repos)))
			}
			p = launch.NewPlan(d, r, ad, cfg)
		}
		p = p.WithSiblings(sib)
		if instructions = strings.TrimSpace(instructions); instructions != "" {
			p = p.WithInstructions(instructions)
		}
		p.HookBin, p.Socket = hookBin, socket
		var warning string
		if !p.InPlace || p.Branch != "" {
			if warning, err = launch.Check(live, p.Worktree, d.Key); err != nil {
				return fail(err)
			}
		}
		rec, err := launch.Run(ctx, p, tm, store)
		return launchedMsg{rec: rec, err: err, claim: claim, ctl: ev, warning: warning}
	}
}

func repoNames(repos []launch.Repo) string {
	var n []string
	for _, r := range repos {
		n = append(n, r.Name)
	}
	return strings.Join(n, ", ")
}

// isCoordinator says whether id is a running project coordinator lanes started.
func (m *Model) isCoordinator(id string) bool {
	return id != "" && slices.ContainsFunc(m.live, func(r state.Record) bool { return r.ID == id && r.Project != "" })
}

// inProject keeps a coordinator to its own project's agents. Other callers aren't held
// to a project.
func (m *Model) inProject(self, ref string) error {
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == self && r.Project != "" })
	if self == "" || i < 0 {
		return nil
	}
	a, err := m.ctlFind(ref)
	if err != nil {
		return err
	}
	project := m.live[i].Project
	if j := slices.IndexFunc(m.issues, func(i tracker.Issue) bool { return i.Key == a.Ticket }); j < 0 || m.issues[j].Project != project {
		return fmt.Errorf("%s isn't on a ticket of %s, the project you coordinate", a.Name, project)
	}
	return nil
}
