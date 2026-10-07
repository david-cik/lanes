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
	hookMsg      hook.Event
	trustMsg     struct{ id, pane string }
	trustGoneMsg struct{ id string }
)

// hookState is what hooks have told lanes about one launched agent.
type hookState struct {
	status   agent.Status
	since    time.Time
	pending  *pending
	trust    bool       // the folder-trust prompt is on screen (hooks don't fire before it)
	activity []activity // newest last, at most activityMax
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
	// Progress, or a new question, means an earlier request was answered elsewhere
	// (in the pane). A later "waiting" notification about the same prompt is not progress.
	if st.pending != nil && (he.Request != nil || he.Status == agent.Working || he.Status == agent.Idle || he.Status == agent.Done) {
		m.release(st, "that request was answered in the agent's pane")
	}
	if he.Status != "" && he.Status != st.status {
		st.status, st.since = he.Status, m.now()
	}
	st.log(m.now(), he.Activity)
	var cmd tea.Cmd
	if he.Request != nil && ev.Reply != nil {
		st.pending = &pending{id: ev.ID, tool: ev.Tool, req: *he.Request, reply: ev.Reply, at: m.now()}
		cmd = m.notify(ev.Agent, fmt.Sprintf("%s: %s", he.Request.Tool, he.Request.Summary))
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

func (m *Model) notify(id, what string) tea.Cmd {
	text := m.whoIs(id) + " needs you — " + what
	cmds := []tea.Cmd{tea.Raw("\a")}
	if n := m.opt.Notify; n != nil {
		cmds = append(cmds, func() tea.Msg { n(text); return nil })
	}
	return tea.Batch(cmds...)
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

// watchTrust looks at a newly launched agent's pane for Claude's folder-trust prompt.
func (m *Model) watchTrust(id, pane string) tea.Cmd {
	tm := m.opt.Tmux
	if tm == nil {
		return nil
	}
	return func() tea.Msg {
		for range 40 {
			out, err := tm.Capture(pane)
			if err != nil {
				return nil
			}
			if trustPrompt(out) {
				return trustMsg{id, pane}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return nil
	}
}

func trustPrompt(screen string) bool {
	s := strings.ToLower(screen)
	return strings.Contains(s, "trust this folder") || strings.Contains(s, "do you trust the files")
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
	m.rebuild()
	m.say("Claude asks whether to trust this new worktree — answer in the right pane (option 2 trusts it)", nil)
	return tea.Batch(m.notify(msg.id, "folder-trust prompt"), m.watchTrustGone(msg.id, msg.pane))
}

// watchTrustGone clears the trust note once the prompt leaves the screen, even if the
// agent then sits idle and no hook fires.
func (m *Model) watchTrustGone(id, pane string) tea.Cmd {
	tm := m.opt.Tmux
	return func() tea.Msg {
		for {
			out, err := tm.Capture(pane)
			if err != nil || !trustPrompt(out) {
				return trustGoneMsg{id}
			}
			time.Sleep(time.Second)
		}
	}
}
