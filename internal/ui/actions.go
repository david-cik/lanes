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
	"github.com/david-cik/lanes/internal/config"
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
		rec     state.Record
		err     error
		resumed string // ended session this resumed; its link is retired
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

// focusSelected is enter: on a lanes agent it shows it; on an ended session it resumes
// it; on a ticket it shows the ticket's lanes agent, or offers to resume one of its
// ended sessions or start a new agent.
func (m *Model) focusSelected() tea.Cmd {
	r, ok := m.selected()
	switch {
	case ok && r.Kind == board.TicketRow:
		return m.openTicket(*r.Issue)
	case ok && r.Kind == board.AgentRow && r.Agent.Ended:
		return m.adoptSelected()
	}
	if a, ok := m.controllable(); ok {
		m.focus(a.RecordID, a.Pane)
	}
	return nil
}

func (m *Model) openTicket(issue tracker.Issue) tea.Cmd {
	var ended []agent.Agent
	for _, a := range m.agents {
		if a.TicketKey != issue.Key {
			continue
		}
		if a.RecordID != "" && m.opt.Tmux != nil && !m.stopping[a.RecordID] {
			m.focus(a.RecordID, a.Pane)
			return nil
		}
		if a.Ended {
			ended = append(ended, a)
		}
	}
	if len(ended) == 0 {
		return m.newAgent()
	}
	items := []choice{{"Start a new agent", ""}}
	for _, a := range ended {
		items = append(items, choice{fmt.Sprintf("Resume %s  %s", a.Name, faint.Render("ended "+ago(m.now().Sub(a.Since)))), a.ID})
	}
	m.modal = &modal{title: issue.Key + " has no running agent", items: items,
		onChoose: func(c choice) tea.Cmd {
			if c.value == "" {
				return m.newAgent()
			}
			for i := range m.rows {
				if a := m.rows[i].Agent; a != nil && a.Ended && a.ID == c.value {
					m.cursor = i
					return m.adoptSelected()
				}
			}
			return nil
		}}
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

type planMsg struct{ plan launch.Plan }

func (m *Model) gotDetail(msg detailMsg) tea.Cmd {
	if msg.err != nil {
		m.say("", msg.err)
		return nil
	}
	m.issueDetails[msg.detail.Key] = cachedIssue{detail: msg.detail}
	if dir := m.opt.Config.LaunchDir; dir != "" {
		return m.folderPlan(msg.detail, msg.adapter, dir)
	}
	return m.worktreePlan(msg.detail, msg.adapter)
}

// folderPlan prepares starting in dir as it is (git lookups off the UI loop).
func (m *Model) folderPlan(d tracker.IssueDetail, a agent.Adapter, dir string) tea.Cmd {
	cfg := m.opt.Config
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return planMsg{launch.FolderPlan(ctx, d, dir, a, cfg)}
	}
}

// pickFolder lets the user start in another folder: the launch folder, a repo as it is,
// or any path they type.
func (m *Model) pickFolder(d tracker.IssueDetail, a agent.Adapter) tea.Cmd {
	var items []choice
	if dir := m.opt.Config.LaunchDir; dir != "" {
		items = append(items, choice{"launch folder  " + faint.Render(home(dir)), dir})
	}
	for _, r := range launch.Repos(m.opt.Config.RepoRoots) {
		items = append(items, choice{r.Name + "  " + faint.Render(home(r.Path)), r.Path})
	}
	items = append(items, choice{"Type a path…", ""})
	m.modal = &modal{title: "Start " + d.Key + " in which folder?", items: items,
		onChoose: func(c choice) tea.Cmd {
			if c.value != "" {
				return m.folderPlan(d, a, c.value)
			}
			m.modal = &modal{input: true, title: "Folder for " + d.Key,
				onInput: func(text string) tea.Cmd {
					dir := config.Expand(strings.TrimSpace(text))
					if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
						m.say("", fmt.Errorf("%s is not a folder", text))
						return nil
					}
					return m.folderPlan(d, a, dir)
				}}
			return nil
		}}
	return nil
}

// worktreePlan prepares a new worktree and branch for the ticket in its repo.
func (m *Model) worktreePlan(d tracker.IssueDetail, a agent.Adapter) tea.Cmd {
	msg := detailMsg{detail: d, adapter: a}
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
	var warning string
	if !p.InPlace || p.Branch != "" { // the lock protects git checkouts only
		w, err := launch.Check(m.live, p.Worktree, p.Ticket.Key)
		if err != nil {
			m.say("", err)
			return nil
		}
		warning = w
	}
	var lines []string
	if p.InPlace {
		lines = []string{"tool:     " + p.Adapter.Name(), "folder:   " + home(p.Worktree)}
		if p.Branch != "" {
			lines = append(lines, "branch:   "+p.Branch+" (as it is)")
		}
	} else {
		lines = []string{
			"tool:     " + p.Adapter.Name(),
			"repo:     " + home(p.Repo.Path),
			"branch:   " + p.Branch + " (new)",
			"worktree: " + home(p.Worktree),
		}
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
	start := func() (tea.Cmd, bool) {
		m.say("starting "+p.Adapter.Name()+" on "+p.Ticket.Key+"…", nil)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			rec, err := launch.Run(ctx, p, tm, store)
			return launchedMsg{rec: rec, err: err}
		}, false
	}
	acts := []action{
		{"y", "start", start},
		{"enter", "start", start},
		{"f", "start in another folder", func() (tea.Cmd, bool) { return m.pickFolder(p.Ticket, p.Adapter), true }},
	}
	md := &modal{title: "Start an agent on " + p.Ticket.Key + "?", lines: lines}
	if p.InPlace {
		acts = append(acts, action{"w", "use a new git worktree for this ticket instead", func() (tea.Cmd, bool) {
			cmd := m.worktreePlan(p.Ticket, p.Adapter)
			return cmd, m.modal != md // close this screen if no repo picker or new plan replaced it
		}})
	}
	md.actions = acts
	m.modal = md
	return nil
}

func (m *Model) launched(msg launchedMsg) tea.Cmd {
	if msg.err != nil {
		m.say("", msg.err)
		return nil
	}
	m.live = append(m.live, msg.rec)
	m.launchSeq = m.agentSeq // refreshes already in flight don't know this agent
	if msg.resumed != "" {   // the fork carries the ticket now; retire the ended session's row
		if l, err := m.opt.Store.DeleteLink(state.LinkKey(msg.rec.Tool, msg.resumed)); err != nil {
			m.say("", err)
		} else {
			m.links = l
		}
	}
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
	what := m.whoIs(rec.ID)
	m.modal = &modal{confirm: true, title: fmt.Sprintf("Stop %s on %s?", rec.Tool, what),
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

// linkSelected (l) puts the selected session on another ticket, or removes it from its
// ticket for good. Works for sessions lanes started (their record changes) and for
// any other session (a saved link).
func (m *Model) linkSelected() tea.Cmd {
	a, ok := m.selectedAgent()
	if !ok {
		m.say("", fmt.Errorf("select a session"))
		return nil
	}
	sess := *a // as shown: TicketKey is resolved
	var items []choice
	if sess.TicketKey != "" {
		items = append(items, choice{"Remove from " + sess.TicketKey, state.NoTicket})
	}
	for _, i := range m.issues {
		if i.Key != sess.TicketKey {
			items = append(items, choice{i.Key + "  " + i.Title, i.Key})
		}
	}
	m.modal = &modal{title: "Ticket for " + sess.Name, items: items,
		onChoose: func(c choice) tea.Cmd {
			err := m.setTicket(sess, c.value)
			m.rebuild()
			if c.value == state.NoTicket {
				m.say("removed "+sess.Name+" from "+sess.TicketKey, err)
			} else {
				m.say("moved "+sess.Name+" to "+c.value, err)
			}
			return m.detailsMoved()
		}}
	return nil
}

func (m *Model) setTicket(a agent.Agent, ticket string) error {
	if a.RecordID == "" {
		return m.setLink(state.LinkKey(a.Tool, a.ID), ticket)
	}
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == a.RecordID })
	if i < 0 {
		return fmt.Errorf("%s is no longer running", a.Name)
	}
	m.live[i].TicketKey = ticket
	for j := range m.agents {
		if m.agents[j].RecordID == a.RecordID {
			m.agents[j].TicketKey = ticket
		}
	}
	return m.opt.Store.Save(m.live[i])
}

// setLink saves one link without overwriting links changed meanwhile by `lanes link`.
func (m *Model) setLink(key, ticket string) error {
	l, err := m.opt.Store.SetLink(key, ticket)
	if err != nil {
		return err
	}
	m.links = l
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
	m.adopting = sess
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
	// The one-agent-per-folder lock protects a git checkout from two agents editing it.
	// A folder that isn't a checkout (e.g. a parent of many repos) has no branch and no lock.
	if p.Branch != "" {
		if _, err := launch.Check(m.live, p.Worktree, p.Ticket.Key); err != nil {
			m.say("", err)
			return nil
		}
	}
	branch := p.Branch
	if branch == "" {
		branch = "(none)"
	}
	ended := m.adopting.Ended && m.adopting.ID == p.ResumeFrom
	lines := []string{
		"tool:   " + p.Adapter.Name(),
		"folder: " + home(p.Worktree),
		"branch: " + branch,
		"",
	}
	title, verb := "Adopt "+name+" for "+p.Ticket.Key+"?", "adopting "
	resumed := ""
	if ended {
		title, verb, resumed = "Resume "+name+" for "+p.Ticket.Key+"?", "resuming ", p.ResumeFrom
		lines = append(lines, "lanes picks this conversation up again in its own pane.")
	} else {
		lines = append(lines, "lanes starts a copy of this conversation in its own pane.",
			"The original keeps running in its terminal — close it when you're done.")
	}
	tm, store := m.opt.Tmux, m.opt.Store
	m.notice = ""
	m.modal = &modal{confirm: true, title: title, lines: lines,
		onYes: func() tea.Cmd {
			m.say(verb+name+"…", nil)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				rec, err := launch.Run(ctx, p, tm, store)
				return launchedMsg{rec, err, resumed}
			}
		}}
	return nil
}
