package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/suggest"
)

// review is the "suggested links" screen.
type review struct {
	items   []reviewItem
	cursor  int
	skipped int // sessions with no ticket mentions
}

type reviewItem struct {
	tool, id, name string
	cands          suggest.Result // best first, at most 3
	pick           int
	on             bool
}

type (
	suggestMsg  review
	autoLinkMsg struct {
		tool, id, name string
		size           int64 // transcript size scored; 0 = none found
		res            suggest.Result
		unchanged      bool // transcript hasn't grown since the last look: not re-read
	}
)

// autoRescan is how often auto_link looks again at a session it couldn't link yet
// (it re-reads only if the transcript grew).
const autoRescan = time.Minute

type autoSeen struct {
	at   time.Time
	size int64
}

// unlinkedClaude lists sessions lanes didn't start that aren't on any open ticket.
// A manual link of "" means the user unlinked the session on purpose: it's offered by T
// but never auto-linked again.
func (m *Model) unlinkedClaude() []agent.Agent {
	open := m.openKeys()
	var out []agent.Agent
	for _, a := range m.agents {
		if a.External && !a.Ended && a.Tool == "claude" && !open[a.TicketKey] && m.links[state.LinkKey(a.Tool, a.ID)] == "" {
			out = append(out, a)
		}
	}
	return out
}

func (m *Model) openKeys() map[string]bool {
	open := map[string]bool{}
	for _, i := range m.issues {
		open[i.Key] = true
	}
	return open
}

// scoreSession reads one session's transcript; a missing transcript just scores nothing.
func scoreSession(a agent.Agent, open map[string]bool) suggest.Result {
	p := suggest.TranscriptPath(os.Getenv("CLAUDE_CONFIG_DIR"), a.Cwd, a.ID)
	if p == "" {
		return nil
	}
	r, _ := suggest.File(p, open)
	return r
}

// suggestLinks scans the transcripts of unlinked Claude sessions for ticket keys.
func (m *Model) suggestLinks() tea.Cmd {
	sessions, open := m.unlinkedClaude(), m.openKeys()
	if len(sessions) == 0 {
		m.say("", fmt.Errorf("no unlinked Claude sessions to look at"))
		return nil
	}
	m.say(fmt.Sprintf("reading %d session transcripts…", len(sessions)), nil)
	return func() tea.Msg {
		var rv review
		for _, a := range sessions {
			res := scoreSession(a, open)
			if len(res) == 0 {
				rv.skipped++
				continue
			}
			if len(res) > 3 {
				res = res[:3]
			}
			rv.items = append(rv.items, reviewItem{tool: a.Tool, id: a.ID, name: a.Name, cands: res, on: res.Confident()})
		}
		return suggestMsg(rv)
	}
}

func (m *Model) gotSuggestions(rv review) {
	if len(rv.items) == 0 {
		m.say("", fmt.Errorf("no open ticket keys found in %d session transcripts", rv.skipped))
		return
	}
	m.notice = ""
	m.review = &rv
}

func (m *Model) reviewKey(k tea.KeyPressMsg) tea.Cmd {
	rv := m.review
	it := &rv.items[rv.cursor]
	switch k.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "q":
		m.review = nil
	case "j", "down":
		rv.cursor = min(rv.cursor+1, len(rv.items)-1)
	case "k", "up":
		rv.cursor = max(rv.cursor-1, 0)
	case "space":
		it.on = !it.on
	case "tab":
		it.pick, it.on = (it.pick+1)%len(it.cands), true
	case "enter":
		n := 0
		var err error
		for _, it := range rv.items {
			if it.on && err == nil {
				err = m.setLink(state.LinkKey(it.tool, it.id), it.cands[it.pick].Key)
				n++
			}
		}
		m.review = nil
		m.rebuild()
		m.say(fmt.Sprintf("linked %d sessions (t on a session changes or removes its link)", n), err)
		return m.detailsMoved()
	}
	return nil
}

func (m *Model) viewReview(b *strings.Builder, body int) {
	rv := m.review
	lines := []string{bold.Render(m.fit("Suggested links from session transcripts"))}
	if rv.skipped > 0 {
		lines = append(lines, faint.Render(m.fit(fmt.Sprintf("%d more sessions mention no open ticket", rv.skipped))))
	}
	for i, it := range rv.items {
		box := "[ ]"
		if it.on {
			box = "[x]"
		}
		c := it.cands[it.pick]
		line := fmt.Sprintf("%s %s → %s %s (%d mentions, %d by you)", box, it.name, c.Key, m.issueTitle(c.Key), c.Mentions, c.Typed)
		line = m.fit(line)
		if i == rv.cursor {
			line = sel.Render(line)
		} else if !it.cands.Confident() {
			line = waitS.Render(line)
		}
		lines = append(lines, line)
		var also []string
		for j, o := range it.cands {
			if j != it.pick {
				also = append(also, fmt.Sprintf("%s (%d)", o.Key, o.Mentions))
			}
		}
		if len(also) > 0 {
			lines = append(lines, faint.Render(m.fit("      also: "+strings.Join(also, ", "))))
		}
	}
	for i := 0; i < body; i++ {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteString("\n")
	}
}

func (m *Model) issueTitle(key string) string {
	for _, i := range m.issues {
		if i.Key == key {
			return i.Title
		}
	}
	return ""
}

// autoLink (auto_link = true) looks at unlinked Claude sessions at most once a minute
// each, re-reading a transcript only when it has grown, and links those that clearly
// point at one open ticket. It only runs while you view your own tickets, and never
// links to an up-for-grabs ticket (one nobody has taken).
func (m *Model) autoLink() tea.Cmd {
	if !m.opt.Config.AutoLink || len(m.issues) == 0 || m.opt.Assignee != m.opt.Config.Assignee {
		return nil
	}
	open, now := m.openKeys(), m.now() // up-for-grabs tickets still score, so they can outweigh yours
	var cmds []tea.Cmd
	for _, a := range m.unlinkedClaude() {
		k := state.LinkKey(a.Tool, a.ID)
		if _, declined := m.links[k]; declined {
			continue
		}
		seen, ok := m.autoChecked[k]
		if ok && now.Sub(seen.at) < autoRescan {
			continue
		}
		m.autoChecked[k] = autoSeen{now, seen.size}
		a, prev := a, seen.size
		cmds = append(cmds, func() tea.Msg {
			p := suggest.TranscriptPath(os.Getenv("CLAUDE_CONFIG_DIR"), a.Cwd, a.ID)
			fi, err := os.Stat(p)
			if p == "" || err != nil {
				return autoLinkMsg{tool: a.Tool, id: a.ID, name: a.Name}
			}
			if fi.Size() == prev {
				return autoLinkMsg{tool: a.Tool, id: a.ID, name: a.Name, size: prev, unchanged: true}
			}
			r, _ := suggest.File(p, open)
			return autoLinkMsg{a.Tool, a.ID, a.Name, fi.Size(), r, false}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) gotAutoLink(msg autoLinkMsg) {
	k := state.LinkKey(msg.tool, msg.id)
	if seen, ok := m.autoChecked[k]; ok {
		m.autoChecked[k] = autoSeen{seen.at, msg.size}
	}
	if msg.unchanged || !msg.res.Confident() || m.isPool(msg.res[0].Key) {
		return
	}
	if _, set := m.links[k]; set { // linked, or unlinked on purpose, meanwhile
		return
	}
	l, set, err := m.opt.Store.SetLinkIfAbsent(k, msg.res[0].Key) // re-checked on disk
	if err == nil {
		m.links = l
	}
	if !set && err == nil {
		m.rebuild()
		return
	}
	m.rebuild()
	m.say(fmt.Sprintf("auto-linked %s → %s (t changes it)", msg.name, msg.res[0].Key), err)
}

// endedSessions turns links to Claude sessions that are no longer running into board
// rows, so you can still find and resume them, until their transcript is older than
// max. Runs off the UI loop (reads transcripts).
func endedSessions(links state.Links, running []agent.Agent, max time.Duration) []agent.Agent {
	alive := map[string]bool{}
	for _, a := range running {
		alive[state.LinkKey(a.Tool, a.ID)] = true
	}
	var out []agent.Agent
	for k, ticket := range links {
		tool, sid, ok := strings.Cut(k, ":")
		if !ok || ticket == "" || ticket == state.NoTicket || tool != "claude" || alive[k] {
			continue
		}
		p := suggest.TranscriptPath(os.Getenv("CLAUDE_CONFIG_DIR"), "", sid)
		if p == "" {
			continue
		}
		in, err := suggest.SessionInfo(p)
		if err != nil || in.Cwd == "" || time.Since(in.When) > max || strings.HasPrefix(in.Entrypoint, "sdk") {
			continue // gone, too old, or run by a tool or plugin rather than a person
		}
		name := in.Title
		if name == "" {
			name = "session " + sid[:min(8, len(sid))]
		}
		out = append(out, agent.Agent{ID: sid, Tool: tool, Name: name, Cwd: in.Cwd, Status: agent.Done,
			Since: in.When, External: true, Ended: true, TicketKey: ticket})
	}
	slices.SortFunc(out, func(a, b agent.Agent) int { return b.Since.Compare(a.Since) })
	return out
}

// toolSessions remembers, per session id, whether a running session was started by a
// tool or plugin rather than a person. Some (a memory plugin's observer, say) report
// themselves as interactive; only their transcript's entrypoint ("sdk-…") tells.
var toolSessions sync.Map

// dropToolSessions leaves out running sessions that tools and plugins started. Each
// transcript is read once; one not written yet is asked about again next refresh.
func dropToolSessions(agents []agent.Agent) []agent.Agent {
	return slices.DeleteFunc(agents, func(a agent.Agent) bool {
		if !a.External || a.Ended || a.Tool != "claude" {
			return false
		}
		if v, ok := toolSessions.Load(a.ID); ok {
			return v.(bool)
		}
		in, err := suggest.SessionInfo(suggest.TranscriptPath(os.Getenv("CLAUDE_CONFIG_DIR"), a.Cwd, a.ID))
		if err != nil || in.Entrypoint == "" {
			return false
		}
		tool := strings.HasPrefix(in.Entrypoint, "sdk")
		toolSessions.Store(a.ID, tool)
		return tool
	})
}
