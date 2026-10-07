package ui

import (
	"fmt"
	"os"
	"strings"

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
		res            suggest.Result
	}
)

// unlinkedClaude lists sessions lanes didn't start that aren't on any open ticket.
func (m *Model) unlinkedClaude() []agent.Agent {
	open := m.openKeys()
	var out []agent.Agent
	for _, a := range m.agents {
		if a.External && a.Tool == "claude" && !open[a.TicketKey] && m.links[state.LinkKey(a.Tool, a.ID)] == "" {
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
		for _, it := range rv.items {
			if it.on {
				m.links[state.LinkKey(it.tool, it.id)] = it.cands[it.pick].Key
				n++
			}
		}
		m.review = nil
		err := m.opt.Store.SaveLinks(m.links)
		m.rebuild()
		m.say(fmt.Sprintf("linked %d sessions (l on a session changes or removes its link)", n), err)
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

// autoLink scores unlinked Claude sessions it hasn't looked at yet (auto_link = true)
// and links the ones whose transcript clearly points at one open ticket.
func (m *Model) autoLink() tea.Cmd {
	if !m.opt.Config.AutoLink || len(m.issues) == 0 {
		return nil
	}
	open := m.openKeys()
	var cmds []tea.Cmd
	for _, a := range m.unlinkedClaude() {
		k := state.LinkKey(a.Tool, a.ID)
		if m.autoChecked[k] {
			continue
		}
		m.autoChecked[k] = true
		a := a
		cmds = append(cmds, func() tea.Msg { return autoLinkMsg{a.Tool, a.ID, a.Name, scoreSession(a, open)} })
	}
	return tea.Batch(cmds...)
}

func (m *Model) gotAutoLink(msg autoLinkMsg) {
	k := state.LinkKey(msg.tool, msg.id)
	if !msg.res.Confident() || m.links[k] != "" {
		return
	}
	m.links[k] = msg.res[0].Key
	err := m.opt.Store.SaveLinks(m.links)
	m.rebuild()
	m.say(fmt.Sprintf("auto-linked %s → %s (l changes it)", msg.name, msg.res[0].Key), err)
}
