package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/state"
)

type (
	hookMsg  hook.Event
	trustMsg struct {
		id, pane string
		t        agent.Truster
	}
	trustGoneMsg struct{ id string }
)

// hookState is what hooks have told lanes about one launched agent.
type hookState struct {
	status   agent.Status
	since    time.Time
	pending  *pending
	trust    bool       // the folder-trust prompt is on screen (hooks don't fire before it)
	ready    bool       // finished a turn while you weren't looking; cleared when you look
	overlap  string     // a ticket whose agent already changed a file this one is changing
	activity []activity // newest last, at most activityMax
	prompted time.Time  // when the tool last said a prompt was submitted
}

type activity struct {
	at   time.Time
	text string
}

const activityMax = 20

func (st *hookState) log(at time.Time, text string) {
	if text == "" {
		return
	}
	st.activity = append(st.activity, activity{at, text})
	if n := len(st.activity); n > activityMax {
		st.activity = st.activity[n-activityMax:]
	}
}

type pending struct {
	id    int
	tool  string
	req   agent.Request
	reply func([]byte)
	at    time.Time
	pick  int    // which suggestion s/A save
	rule  string // edited rule text for the picked suggestion; "" = unchanged
}

func (m *Model) waitHook() tea.Cmd {
	ch := m.opt.Hooks
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return hookMsg(ev)
	}
}

func (m *Model) hook(ev hook.Event) tea.Cmd {
	if ev.Control != "" {
		return m.control(ev)
	}
	st := m.hooks[ev.Agent]
	if st == nil {
		st = &hookState{}
		m.hooks[ev.Agent] = st
	}
	if ev.Closed { // the hook gave up waiting; the tool's own prompt is still there
		if st.pending != nil && st.pending.id == ev.ID {
			m.release(st, "")
		}
		m.rebuild()
		return nil
	}
	h, ok := m.adapter(ev.Tool).(agent.Hooker)
	if !ok {
		reply(ev, nil)
		return nil
	}
	he, err := h.ParseHook(ev.Event, ev.Payload)
	if err != nil {
		reply(ev, nil)
		m.say("", err)
		return nil
	}
	st.trust = false
	if ev.Event == "UserPromptSubmit" {
		st.prompted = m.now()
	}
	m.learnSession(ev.Agent, he.SessionID)
	// Progress, or a new question, means an earlier request was answered elsewhere
	// (in the pane). A later "waiting" notification about the same prompt is not progress.
	if st.pending != nil && (he.Request != nil || he.Status == agent.Working || he.Status == agent.Idle || he.Status == agent.Done) {
		m.release(st, "that request was answered in the agent's pane")
	}
	var cmd tea.Cmd
	if he.Status != "" && he.Status != st.status {
		if st.status == agent.Working && he.Status == agent.Idle { // a turn finished
			cmd = m.turnDone(ev.Agent, st)
			m.event("ready", ev.Agent, "")
		}
		if he.Status == agent.Done {
			m.event("ended", ev.Agent, "")
		}
		if he.Status != agent.Idle { // working again, waiting, or ended: not "ready" any more
			st.ready = false
		}
		st.status, st.since = he.Status, m.now()
	}
	st.log(m.now(), he.Activity)
	if len(he.Writes) > 0 {
		cmd = tea.Batch(cmd, m.noteWrites(ev.Agent, st, he.Writes))
	}
	if he.Request != nil && ev.Reply != nil {
		st.pending = &pending{id: ev.ID, tool: ev.Tool, req: *he.Request, reply: ev.Reply, at: m.now()}
		cmd = m.notify(ev.Agent, fmt.Sprintf("%s: %s", he.Request.Tool, he.Request.Summary))
		m.event("needs_you", ev.Agent, he.Request.Tool+": "+he.Request.Summary)
	} else {
		reply(ev, nil)
	}
	m.rebuild()
	return cmd
}

// release lets go of a pending request without a decision and closes any dialog
// about it, so a key press can never answer a request the user didn't see.
func (m *Model) release(st *hookState, why string) {
	p := st.pending
	p.reply(nil)
	st.pending = nil
	if m.modal != nil && m.modal.id == approvalID(p) {
		m.modal = nil
		if why != "" {
			m.say(why, nil)
		}
	}
}

func approvalID(p *pending) string { return fmt.Sprintf("approval-%d", p.id) }

func reply(ev hook.Event, b []byte) {
	if ev.Reply != nil {
		ev.Reply(b)
	}
}

// notify rings the terminal bell and tells the user via tmux / the OS.
// hookKey is the id hook events use for an agent: its launch record for agents lanes
// started, "<tool>:<session id>" for sessions reporting through global hooks.
func hookKey(a *agent.Agent) string {
	if a.RecordID != "" {
		return a.RecordID
	}
	return a.Tool + ":" + a.ID
}

// whoIs names the agent behind a hook key for messages: its ticket, else its name.
func (m *Model) whoIs(id string) string {
	if i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == id }); i >= 0 && m.live[i].TicketKey != state.NoTicket {
		return m.live[i].TicketKey
	}
	for i := range m.agents {
		if a := &m.agents[i]; hookKey(a) == id {
			if a.TicketKey != "" && a.TicketKey != state.NoTicket {
				return a.TicketKey
			}
			return a.Name
		}
	}
	return id
}

// turnDone marks an agent lanes started as ready for you when it finishes a turn, and
// says so, unless it's the one on screen (you can see it). Sessions started elsewhere
// stay quiet: you're in their terminal.
func (m *Model) turnDone(id string, st *hookState) tea.Cmd {
	if !slices.ContainsFunc(m.live, func(r state.Record) bool { return r.ID == id }) || m.shown == id {
		return nil
	}
	st.ready = true
	return m.ping(m.whoIs(id) + " is ready for you")
}

func (m *Model) notify(id, what string) tea.Cmd {
	return m.ping(m.whoIs(id) + " needs you — " + what)
}

// termNoticeMsg carries a terminal notification, worked out off the UI loop, to be
// written out.
type termNoticeMsg struct{ seq string }

// ping rings the terminal bell and shows text as a tmux (and, if on, desktop) notice.
// Whether the terminal shows the desktop notice itself is decided once, in the
// background (it asks tmux), and the system notifier is used only if it doesn't.
func (m *Model) ping(text string) tea.Cmd {
	term, n := m.opt.TermNotify, m.opt.Notify
	return tea.Batch(tea.Raw("\a"), func() tea.Msg {
		proto := ""
		if term != nil {
			proto = term()
		}
		seq := termNotice(proto, text)
		if n != nil {
			n(text, seq != "")
		}
		if seq == "" {
			return nil
		}
		return termNoticeMsg{seq}
	})
}

// termNotice is a desktop notification the terminal itself shows, in the escape code
// it understands ("osc9", "osc99", "osc777"; "" = none), wrapped to pass through tmux.
// Control characters are dropped from the text so a ticket title can't end it early.
func termNotice(protocol, text string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ';' && protocol == "osc777" {
			return -1
		}
		return r
	}, text)
	var seq string
	switch protocol {
	case "osc9":
		seq = "\x1b]9;lanes: " + clean + "\x07"
	case "osc99": // kitty
		seq = "\x1b]99;;lanes: " + clean + "\x1b\\"
	case "osc777":
		seq = "\x1b]777;notify;lanes;" + clean + "\x07"
	default:
		return ""
	}
	// tmux passes a sequence through when it's wrapped in DCS tmux; … ST, with every
	// ESC inside doubled.
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// forgetHooks drops state for agents that are gone, releasing any waiting hook.
func (m *Model) forgetHooks() {
	known := map[string]bool{}
	for _, r := range m.live {
		known[r.ID] = true
	}
	for i := range m.agents {
		known[hookKey(&m.agents[i])] = true
	}
	for id, st := range m.hooks {
		if !known[id] {
			if st.pending != nil {
				m.release(st, "")
			}
			delete(m.hooks, id)
		}
	}
}

// overlay applies hook status to launched agents (hooks are fresher than polling).
func (m *Model) overlay(agents []agent.Agent) {
	for i := range agents {
		st := m.hooks[hookKey(&agents[i])]
		if st == nil {
			continue
		}
		if st.trust {
			agents[i].Status = agent.Waiting
		} else if st.status != "" {
			agents[i].Status, agents[i].Since = st.status, st.since
		}
	}
}

func (m *Model) readyCount() int {
	n := 0
	for _, st := range m.hooks {
		if st.ready {
			n++
		}
	}
	return n
}

func (m *Model) waitingCount() int {
	n := 0
	for _, st := range m.hooks {
		if st.pending != nil || st.trust {
			n++
		}
	}
	return n
}

// waitingNote is the extra text on a waiting agent's row.
func (m *Model) waitingNote(a *agent.Agent) string {
	st := m.hooks[hookKey(a)]
	switch {
	case st == nil:
		return ""
	case st.trust:
		return " · answer the folder-trust prompt in the pane"
	case st.pending != nil:
		return " · " + st.pending.req.Tool + ": " + st.pending.req.Summary
	}
	return ""
}

// --- approvals ---

func (m *Model) approve() tea.Cmd {
	id := ""
	if a, ok := m.selectedAgent(); ok && m.hooks[hookKey(a)] != nil && m.hooks[hookKey(a)].pending != nil {
		id = hookKey(a)
	} else {
		var oldest time.Time
		for rid, st := range m.hooks {
			if st.pending != nil && (id == "" || st.pending.at.Before(oldest)) {
				id, oldest = rid, st.pending.at
			}
		}
	}
	if id == "" {
		for _, st := range m.hooks {
			if st.trust {
				m.say("", fmt.Errorf("the folder-trust prompt can only be answered in the agent's pane"))
				return nil
			}
		}
		m.say("", fmt.Errorf("nothing is waiting for approval"))
		return nil
	}
	m.openApproval(id)
	return nil
}

func (m *Model) openApproval(id string) {
	st := m.hooks[id]
	if st == nil || st.pending == nil {
		return
	}
	p := st.pending
	ticket := m.whoIs(id)
	lines := []string{
		"tool:  " + p.req.Tool,
		"input: " + p.req.Summary,
	}
	sug := p.req.Suggestions
	for i, s := range sug {
		mark := "  "
		if i == p.pick {
			mark = "▸ "
		}
		label := s.Label
		if i == p.pick && p.rule != "" {
			label += "  →  " + p.rule
		}
		lines = append(lines, mark+"rule: "+label)
	}
	lines = append(lines, "")
	acts := []action{
		{"y", "allow once", func() (tea.Cmd, bool) { return m.decide(id, p, agent.Decision{Behavior: "allow"}), false }},
	}
	if len(sug) > 0 {
		save := func(to string) func() (tea.Cmd, bool) {
			return func() (tea.Cmd, bool) {
				s := sug[p.pick]
				if p.rule != "" {
					s.Rule = p.rule
				}
				return m.decide(id, p, agent.Decision{Behavior: "allow", Save: &s, SaveTo: to}), false
			}
		}
		acts = append(acts,
			action{"s", "allow + remember for this session", save("session")},
			action{"A", "allow + always allow in this repo", save("localSettings")})
		if sug[p.pick].Rule != "" {
			acts = append(acts, action{"e", "edit the rule", func() (tea.Cmd, bool) {
				cur := sug[p.pick].Rule
				if p.rule != "" {
					cur = p.rule
				}
				m.modal = &modal{id: approvalID(p), input: true, text: cur, title: "Rule for " + sug[p.pick].Label,
					onInput: func(text string) tea.Cmd { p.rule = text; m.openApproval(id); return nil }}
				return nil, true
			}})
		}
		if len(sug) > 1 {
			acts = append(acts, action{"tab", "next rule", func() (tea.Cmd, bool) {
				p.pick, p.rule = (p.pick+1)%len(sug), ""
				m.openApproval(id)
				return nil, true
			}})
		}
	}
	acts = append(acts,
		action{"n", "deny", func() (tea.Cmd, bool) { return m.decide(id, p, agent.Decision{Behavior: "deny"}), false }},
		action{"m", "deny with a message", func() (tea.Cmd, bool) {
			m.modal = &modal{id: approvalID(p), input: true, title: "Tell the agent why",
				onInput: func(text string) tea.Cmd { return m.decide(id, p, agent.Decision{Behavior: "deny", Message: text}) }}
			return nil, true
		}},
		action{"p", "answer in the pane", func() (tea.Cmd, bool) {
			if st.pending != p {
				m.say("", fmt.Errorf("that request was already answered"))
				return nil, false
			}
			p.reply(nil)
			st.pending = nil
			st.log(m.now(), "you chose to answer in the pane")
			if i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == id }); i >= 0 && m.opt.Tmux != nil {
				m.focus(id, m.live[i].Pane)
			}
			m.rebuild()
			return nil, false
		}},
	)
	m.modal = &modal{id: approvalID(p), title: ticket + " wants permission", lines: lines, actions: acts,
		hint: "esc close (the request stays waiting)"}
}

// decide answers request p, and only p: if the agent has moved on to another request
// meanwhile, nothing is sent.
func (m *Model) decide(id string, p *pending, d agent.Decision) tea.Cmd {
	st := m.hooks[id]
	if st == nil || st.pending != p {
		m.say("", fmt.Errorf("that request was already answered"))
		return nil
	}
	h, _ := m.adapter(st.pending.tool).(agent.Hooker)
	if h != nil {
		st.pending.reply(h.EncodeDecision(d))
	} else {
		st.pending.reply(nil)
	}
	st.pending = nil
	st.status, st.since = agent.Working, m.now()
	switch {
	case d.Behavior == "deny" && d.Message != "":
		st.log(m.now(), "you denied: "+strings.Join(strings.Fields(d.Message), " "))
	case d.Behavior == "deny":
		st.log(m.now(), "you denied it")
	case d.Save != nil:
		st.log(m.now(), "you allowed it and saved "+d.Save.Label)
	default:
		st.log(m.now(), "you allowed it")
	}
	verb := d.Behavior + "ed"
	if d.Behavior == "deny" {
		verb = "denied"
	}
	if d.Save != nil {
		verb += " (rule saved to " + map[string]string{"session": "this session", "localSettings": "the repo's local settings"}[d.SaveTo] + ")"
	}
	m.say(verb, nil)
	m.rebuild()
	return nil
}

// --- folder trust ---

// watchTrust looks at a newly launched agent's pane for its tool's folder-trust prompt.
func (m *Model) watchTrust(id, pane string, t agent.Truster) tea.Cmd {
	tm := m.opt.Tmux
	if tm == nil || t == nil {
		return nil
	}
	return func() tea.Msg {
		for range 40 {
			out, err := tm.Capture(pane)
			if err != nil {
				return nil
			}
			if t.TrustPrompt(out) {
				return trustMsg{id, pane, t}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return nil
	}
}

func (m *Model) trusted(msg trustMsg) tea.Cmd {
	st := m.hooks[msg.id]
	if st == nil {
		st = &hookState{}
		m.hooks[msg.id] = st
	}
	if st.status != "" { // a hook already fired, so trust was already given
		return nil
	}
	st.trust = true
	m.event("needs_you", msg.id, "folder-trust prompt")
	m.rebuild()
	m.say(m.whoIs(msg.id)+" asks whether to trust this new folder — answer in its pane", nil)
	return tea.Batch(m.notify(msg.id, "folder-trust prompt"), m.watchTrustGone(msg.id, msg.pane, msg.t))
}

// watchTrustGone clears the trust note once the prompt leaves the screen, even if the
// agent then sits idle and no hook fires.
func (m *Model) watchTrustGone(id, pane string, t agent.Truster) tea.Cmd {
	tm := m.opt.Tmux
	return func() tea.Msg {
		for {
			out, err := tm.Capture(pane)
			if err != nil || !t.TrustPrompt(out) {
				return trustGoneMsg{id}
			}
			time.Sleep(time.Second)
		}
	}
}

// paneChanges does for agents whose status comes from their pane (tools without hooks)
// what hooks do for the rest: a turn that ends makes the agent ready, and needing you
// says so.
func (m *Model) paneChanges(next []agent.Agent) tea.Cmd {
	was := map[string]agent.Status{}
	for _, a := range m.agents {
		if a.RecordID != "" {
			was[a.RecordID] = a.Status
		}
	}
	var cmds []tea.Cmd
	for _, a := range next {
		prev, seen := was[a.RecordID]
		if _, hooks := m.adapter(a.Tool).(agent.Hooker); a.RecordID == "" || hooks || !seen || prev == a.Status {
			continue
		}
		st := m.hooks[a.RecordID]
		if st == nil {
			st = &hookState{}
			m.hooks[a.RecordID] = st
		}
		if a.Status != agent.Idle {
			st.ready = false
		}
		switch {
		case prev == agent.Working && a.Status == agent.Idle:
			m.event("ready", a.RecordID, "")
			cmds = append(cmds, m.turnDone(a.RecordID, st))
		case a.Status == agent.Waiting:
			st.log(m.now(), "waiting for you (see its pane)")
			m.event("needs_you", a.RecordID, "see its pane")
			cmds = append(cmds, m.notify(a.RecordID, "see its pane"))
		}
	}
	return tea.Batch(cmds...)
}

// learnSession saves the session id a tool reported, for an agent lanes started without
// one (the tool can't be given an id at launch), so its transcript can be found.
func (m *Model) learnSession(id, session string) {
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == id })
	if session == "" || i < 0 || m.live[i].SessionID != "" {
		return
	}
	if err := m.opt.Store.SetRecordSession(id, session); err != nil {
		m.say("", err)
		return
	}
	m.live[i].SessionID = session
}

// sentCheck is a look, a moment after lanes sent an agent a message, at whether the agent
// took it.
type sentCheck struct {
	id, tool, pane string
	at             time.Time
	tries          int
}

// sendRetries is how many more times lanes presses Enter for a message that didn't go.
const sendRetries = 2

func (m *Model) confirmSent(id, tool, pane, text string) tea.Cmd {
	if _, hooks := m.adapter(tool).(agent.Hooker); !hooks {
		return nil // nothing would say whether it went
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return nil // a command (/model, /resume) may open a picker with no prompt submitted: Enter would choose in it
	}
	c := sentCheck{id, tool, pane, m.now(), 0}
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return c })
}

// checkSent presses Enter again if the agent didn't take the message: its tool said no
// prompt was submitted since, and it sits idle. An Enter can land as part of the message
// (a newline), leaving it unsent in the agent's input box; another Enter on an empty box
// does nothing. Only when idle: a busy agent has queued the message, and an agent showing
// a dialog (a permission, a question, folder trust) would take Enter as its answer.
func (m *Model) checkSent(c sentCheck) tea.Cmd {
	st := m.hooks[c.id]
	if st == nil || st.prompted.After(c.at) || st.status != agent.Idle || st.pending != nil || st.trust {
		return nil
	}
	if !slices.ContainsFunc(m.live, func(r state.Record) bool { return r.Pane == c.pane }) {
		return nil // stopped meanwhile
	}
	if c.tries >= sendRetries {
		m.say("", fmt.Errorf("%s hasn't started on the message; press Enter in its pane", m.whoIs(c.id)))
		return nil
	}
	c.tries++
	tm := m.opt.Tmux
	return tea.Batch(func() tea.Msg { tm.SendKeys(c.pane, "Enter"); return nil },
		tea.Tick(2*time.Second, func(time.Time) tea.Msg { return c }))
}
