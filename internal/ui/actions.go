package ui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/launch"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

type (
	detailMsg struct {
		detail  tracker.IssueDetail
		adapter agent.Adapter
		err     error
	}
	launchedMsg struct {
		rec state.Record
		err error
	}
)

const slotPercent = 65

// selectedAgent returns the agent under the cursor, if any.
func (m *Model) selectedAgent() (*agent.Agent, bool) {
	r, ok := m.selected()
	if !ok || r.Kind != board.AgentRow {
		return nil, false
	}
	return r.Agent, true
}

// selectedIssue returns the ticket under the cursor, or the ticket of the agent under it.
func (m *Model) selectedIssue() (tracker.Issue, bool) {
	r, ok := m.selected()
	if !ok {
		return tracker.Issue{}, false
	}
	key := ""
	switch r.Kind {
	case board.TicketRow:
		return *r.Issue, true
	case board.AgentRow:
		key = r.Agent.TicketKey
	}
	for _, i := range m.issues {
		if i.Key == key {
			return i, true
		}
	}
	return tracker.Issue{}, false
}

// controllable returns the agent under the cursor if lanes launched it and runs in tmux.
func (m *Model) controllable() (*agent.Agent, bool) {
	a, ok := m.selectedAgent()
	switch {
	case !ok:
		m.say("", fmt.Errorf("select an agent"))
	case a.RecordID == "":
		m.say("", fmt.Errorf("%s was not started by lanes, so lanes can't control it (A adopts it, l links it to a ticket)", a.Name))
	case m.stopping[a.RecordID]:
		m.say("", fmt.Errorf("%s is stopping", a.Name))
	case m.opt.Tmux == nil:
		m.say("", fmt.Errorf("run lanes inside tmux to control agents"))
	default:
		return a, true
	}
	return nil, false
}

func (m *Model) adapter(name string) agent.Adapter {
	for _, a := range m.opt.Adapters {
		if a.Name() == name {
			return a
		}
	}
	return nil
}

// --- right slot ---

func (m *Model) focusSelected() tea.Cmd {
	if a, ok := m.controllable(); ok {
		m.focus(a.RecordID, a.Pane)
	}
	return nil
}

// focus shows an agent in the right slot: the current one goes home first, so every
// agent not on screen sits alone in its own lanes-<id> session.
func (m *Model) focus(id, pane string) {
	tm := m.opt.Tmux
	if m.shown != id {
		if err := m.unshow(); err != nil {
			m.say("", err) // the slot is not in a known state; don't move more panes
			return
		}
		if err := tm.Swap(pane, m.opt.Placeholder); err != nil {
			m.say("", err)
			return
		}
		m.shown, m.shownPane = id, pane
	}
	if err := tm.Select(pane); err != nil {
		m.say("", err)
	}
}

// unshow puts the placeholder back in the slot. If the shown agent already exited,
// its pane is gone and the placeholder sits alone in that agent's old session, so it
// is joined back next to the panel instead.
func (m *Model) unshow() error {
	if m.shown == "" || m.opt.Tmux == nil {
		return nil
	}
	tm := m.opt.Tmux
	err := tm.Swap(m.shownPane, m.opt.Placeholder)
	if err != nil {
		err = tm.Join(m.opt.Placeholder, m.opt.Panel, slotPercent)
	}
	m.shown, m.shownPane = "", ""
	return err
}

// dropVanishedShown handles the shown agent exiting while on screen: its pane closed
// in the panel window and the placeholder was left in the agent's home session.
func (m *Model) dropVanishedShown() {
	if m.shown == "" {
		return
	}
	for _, r := range m.live {
		if r.ID == m.shown {
			m.shownPane = r.Pane
			return
		}
	}
	m.shown, m.shownPane = "", ""
	if err := m.opt.Tmux.Join(m.opt.Placeholder, m.opt.Panel, slotPercent); err != nil {
		m.say("", err)
	}
}

// forgetStopped drops "stopping" marks for agents that are gone.
func (m *Model) forgetStopped() {
	for id := range m.stopping {
		if !slices.ContainsFunc(m.live, func(r state.Record) bool { return r.ID == id }) {
			delete(m.stopping, id)
		}
	}
}

// --- launch ---

func (m *Model) newAgent() tea.Cmd {
	if m.opt.Tmux == nil {
		m.say("", fmt.Errorf("run lanes inside tmux to launch agents"))
		return nil
	}
	issue, ok := m.selectedIssue()
	if !ok {
		m.say("", fmt.Errorf("select a ticket"))
		return nil
	}
	ads := slices.Clone(m.opt.Adapters)
	slices.SortStableFunc(ads, func(a, b agent.Adapter) int { // default agent first
		return boolInt(b.Name() == m.opt.Config.DefaultAgent) - boolInt(a.Name() == m.opt.Config.DefaultAgent)
	})
	if len(ads) == 1 {
		return m.fetchDetail(issue, ads[0])
	}
	var items []choice
	for _, a := range ads {
		items = append(items, choice{a.Name(), a.Name()})
	}
	m.modal = &modal{title: "New agent on " + issue.Key + " — which tool?", items: items,
		onChoose: func(c choice) tea.Cmd { return m.fetchDetail(issue, m.adapter(c.value)) }}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (m *Model) fetchDetail(issue tracker.Issue, a agent.Adapter) tea.Cmd {
	m.say("loading "+issue.Key+"…", nil)
	tr := m.opt.Tracker
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := tr.Issue(ctx, issue.Key)
		return detailMsg{d, a, err}
	}
}

func (m *Model) gotDetail(msg detailMsg) tea.Cmd {
	if msg.err != nil {
		m.say("", msg.err)
		return nil
	}
	m.issueDetails[msg.detail.Key] = cachedIssue{detail: msg.detail}
	repos := launch.Repos(m.opt.Config.RepoRoots)
	if r, ok := launch.InferRepo(msg.detail, repos); ok {
		return m.confirmLaunch(launch.NewPlan(msg.detail, r, msg.adapter, m.opt.Config))
	}
	if len(repos) == 0 {
		m.say("", fmt.Errorf("no git repos found under repo_roots %v", m.opt.Config.RepoRoots))
		return nil
	}
	var items []choice
	for _, r := range repos {
		items = append(items, choice{r.Name + "  " + faint.Render(home(r.Path)), r.Path})
	}
	m.notice = ""
	m.modal = &modal{title: "Which repo for " + msg.detail.Key + "?", items: items,
		onChoose: func(c choice) tea.Cmd {
			i := slices.IndexFunc(repos, func(r launch.Repo) bool { return r.Path == c.value })
			return m.confirmLaunch(launch.NewPlan(msg.detail, repos[i], msg.adapter, m.opt.Config))
		}}
	return nil
}

func (m *Model) confirmLaunch(p launch.Plan) tea.Cmd {
	if _, ok := p.Adapter.(agent.Hooker); ok {
		p.HookBin, p.Socket = m.opt.HookBin, m.opt.Socket
	}
	warning, err := launch.Check(m.live, p.Worktree, p.Ticket.Key)
	if err != nil {
		m.say("", err)
		return nil
	}
	lines := []string{
		"tool:     " + p.Adapter.Name(),
		"repo:     " + home(p.Repo.Path),
		"branch:   " + p.Branch,
		"worktree: " + home(p.Worktree),
	}
	if warning != "" {
		lines = append(lines, "", waitS.Render("⚠ "+warning))
	}
	if _, err := os.Stat(p.Worktree); err != nil && p.Adapter.Name() == "claude" {
		lines = append(lines, "", "Claude will ask whether to trust this new folder; answer in the right pane.")
	}
	if strings.Contains(p.HookBin, "go-build") {
		lines = append(lines, "", waitS.Render("⚠ lanes is running via `go run`: the agent's hooks will break once it exits (use go install)"))
	}
	m.notice = ""
	tm, store := m.opt.Tmux, m.opt.Store
	m.modal = &modal{confirm: true, title: "Start an agent on " + p.Ticket.Key + "?", lines: lines,
		onYes: func() tea.Cmd {
			m.say("starting "+p.Adapter.Name()+" on "+p.Ticket.Key+"…", nil)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				rec, err := launch.Run(ctx, p, tm, store)
				return launchedMsg{rec, err}
			}
		}}
	return nil
}

func (m *Model) launched(msg launchedMsg) tea.Cmd {
	if msg.err != nil {
		m.say("", msg.err)
		return nil
	}
	m.live = append(m.live, msg.rec)
	m.launchSeq = m.agentSeq // refreshes already in flight don't know this agent
	m.focus(msg.rec.ID, msg.rec.Pane)
	m.say(fmt.Sprintf("started %s on %s in %s", msg.rec.Tool, msg.rec.TicketKey, home(msg.rec.Worktree)), nil)
	return tea.Batch(m.fetchAgents(), m.watchTrust(msg.rec.ID, msg.rec.Pane))
}

// --- send / stop / link ---

func (m *Model) sendSelected() tea.Cmd {
	a, ok := m.controllable()
	if !ok {
		return nil
	}
	tm, pane := m.opt.Tmux, a.Pane
	m.modal = &modal{input: true, title: "Send to " + a.Name,
		onInput: func(text string) tea.Cmd {
			if text == "" {
				return nil
			}
			return func() tea.Msg { return noticeMsg{"sent", tm.SendText(pane, text)} }
		}}
	return nil
}

func (m *Model) stopSelected() tea.Cmd {
	a, ok := m.controllable()
	if !ok {
		return nil
	}
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == a.RecordID })
	if i < 0 {
		return nil
	}
	rec, ad, tm, store := m.live[i], m.adapter(a.Tool), m.opt.Tmux, m.opt.Store
	m.modal = &modal{confirm: true, title: fmt.Sprintf("Stop %s on %s?", rec.Tool, rec.TicketKey),
		lines: []string{"The worktree and branch are kept: " + home(rec.Worktree)},
		onYes: func() tea.Cmd {
			if m.shown == rec.ID {
				if err := m.unshow(); err != nil {
					m.say("", err)
				}
			}
			m.stopping[rec.ID] = true
			m.say("stopping "+rec.Tool+" on "+rec.TicketKey+"…", nil)
			return func() tea.Msg {
				return noticeMsg{"stopped " + rec.Tool + " on " + rec.TicketKey, stop(rec, ad, tm, store)}
			}
		}}
	return nil
}

// stop asks the tool to exit, waits up to 10s, then kills the session.
func stop(rec state.Record, ad agent.Adapter, tm Tmux, store state.Store) error {
	if ad != nil {
		text, keys := ad.Stop()
		if text != "" {
			tm.SendText(rec.Pane, text)
		}
		if len(keys) > 0 {
			tm.SendKeys(rec.Pane, keys...)
		}
	}
	for range 20 {
		if !tm.HasSession(rec.Session()) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if tm.HasSession(rec.Session()) {
		if err := tm.KillSession(rec.Session()); err != nil {
			return err
		}
	}
	return store.Delete(rec.ID)
}

func (m *Model) linkSelected() tea.Cmd {
	a, ok := m.selectedAgent()
	if !ok {
		m.say("", fmt.Errorf("select an agent session to link"))
		return nil
	}
	if a.RecordID != "" {
		m.say("", fmt.Errorf("lanes started this agent for %s; its ticket is fixed", a.TicketKey))
		return nil
	}
	key := state.LinkKey(a.Tool, a.ID)
	var items []choice
	if _, linked := m.links[key]; linked {
		items = append(items, choice{"(unlink — go back to matching by branch/name)", ""})
	}
	for _, i := range m.issues {
		items = append(items, choice{i.Key + "  " + i.Title, i.Key})
	}
	name := a.Name
	m.modal = &modal{title: "Link " + name + " to which ticket?", items: items,
		onChoose: func(c choice) tea.Cmd {
			if c.value == "" {
				delete(m.links, key)
			} else {
				m.links[key] = c.value
			}
			err := m.opt.Store.SaveLinks(m.links)
			m.rebuild()
			if c.value == "" {
				m.say("unlinked "+name, err)
			} else {
				m.say("linked "+name+" to "+c.value, err)
			}
			return nil
		}}
	return nil
}

// --- adopt ---

type adoptPlanMsg struct {
	plan launch.Plan
	name string
}

// adoptSelected forks a session lanes didn't start into a lanes-managed pane, in the
// session's own directory. The original keeps running untouched.
func (m *Model) adoptSelected() tea.Cmd {
	a, ok := m.selectedAgent()
	switch {
	case !ok:
		m.say("", fmt.Errorf("select a session to adopt"))
		return nil
	case !a.External:
		m.say("", fmt.Errorf("lanes already runs %s", a.Name))
		return nil
	case m.opt.Tmux == nil:
		m.say("", fmt.Errorf("run lanes inside tmux to adopt sessions"))
		return nil
	case a.Cwd == "":
		m.say("", fmt.Errorf("%s has no known directory", a.Name))
		return nil
	}
	ad := m.adapter(a.Tool)
	if f, ok := ad.(agent.Forker); !ok || !f.CanFork() {
		m.say("", fmt.Errorf("lanes can't adopt %s sessions", a.Tool))
		return nil
	}
	sess := *a
	cfg := m.opt.Config
	adopt := func(issue tracker.Issue) tea.Cmd {
		d := tracker.IssueDetail{Issue: issue}
		if c, ok := m.issueDetails[issue.Key]; ok && c.err == nil {
			d = c.detail
		}
		return func() tea.Msg { // git lookups off the UI loop
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return adoptPlanMsg{launch.AdoptPlan(ctx, d, sess.Cwd, sess.ID, ad, cfg), sess.Name}
		}
	}
	for _, i := range m.issues {
		if i.Key == a.TicketKey {
			return adopt(i)
		}
	}
	var items []choice
	for _, i := range m.issues {
		items = append(items, choice{i.Key + "  " + i.Title, i.Key})
	}
	m.modal = &modal{title: "Adopt " + a.Name + " — for which ticket?", items: items,
		onChoose: func(c choice) tea.Cmd {
			for _, i := range m.issues {
				if i.Key == c.value {
					return adopt(i)
				}
			}
			return nil
		}}
	return nil
}

func (m *Model) confirmAdopt(p launch.Plan, name string) tea.Cmd {
	if _, ok := p.Adapter.(agent.Hooker); ok {
		p.HookBin, p.Socket = m.opt.HookBin, m.opt.Socket
	}
	if _, err := launch.Check(m.live, p.Worktree, p.Ticket.Key); err != nil {
		m.say("", err)
		return nil
	}
	branch := p.Branch
	if branch == "" {
		branch = "(none)"
	}
	lines := []string{
		"tool:   " + p.Adapter.Name(),
		"folder: " + home(p.Worktree),
		"branch: " + branch,
		"",
		"lanes starts a copy of this conversation in its own pane.",
		"The original keeps running in its terminal — close it when you're done.",
	}
	tm, store := m.opt.Tmux, m.opt.Store
	m.notice = ""
	m.modal = &modal{confirm: true, title: "Adopt " + name + " for " + p.Ticket.Key + "?", lines: lines,
		onYes: func() tea.Cmd {
			m.say("adopting "+name+"…", nil)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				rec, err := launch.Run(ctx, p, tm, store)
				return launchedMsg{rec, err}
			}
		}}
	return nil
}
