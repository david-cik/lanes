package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/tracker"
)

// ticketCard (i) shows the selected ticket — or the ticket of the selected agent — on
// the whole panel: its state, project, pull requests, agents and full description.
func (m *Model) ticketCard() tea.Cmd {
	issue, ok := m.selectedIssue()
	if !ok {
		m.say("", fmt.Errorf("select a ticket (or one of its agents)"))
		return nil
	}
	m.notice = ""
	m.showCard(issue)
	if _, ok := m.issueDetails[issue.Key]; ok {
		return nil
	}
	tr, key := m.opt.Tracker, issue.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := tr.Issue(ctx, key)
		return issueMsg{key, d, err}
	}
}

func cardID(key string) string { return "card:" + key }

// mentionTag is a tracker mention in a description, e.g. <issue id=… href=…>ABC-3</issue>;
// the card shows just its text.
var mentionTag = regexp.MustCompile(`<(issue|user|project|document|team)\b[^>]*>(.*?)</(?:issue|user|project|document|team)>`)

// showCard opens (or, once details arrive, refills) the card, keeping its scroll.
func (m *Model) showCard(i tracker.Issue) {
	meta := []string{i.Team, i.State}
	if i.Project != "" {
		meta = append(meta, i.Project)
	}
	lines := []string{faint.Render(strings.Join(meta, " · ")), faint.Render(i.URL), ""}
	c, ok := m.issueDetails[i.Key]
	lines = append(lines, sect.Render("pull requests"))
	switch {
	case !ok:
		lines = append(lines, faint.Render("  loading…"))
	case c.err != nil:
		lines = append(lines, faint.Render("  "+firstLine(c.err.Error())))
	case len(c.detail.PRURLs) == 0:
		lines = append(lines, faint.Render("  none linked"))
	default:
		for _, u := range c.detail.PRURLs {
			lines = append(lines, "  "+clean(u))
		}
	}
	lines = append(lines, "", sect.Render("agents"))
	n := 0
	for _, a := range m.agents {
		if a.TicketKey == i.Key {
			n++
			lines = append(lines, fmt.Sprintf("  %s %s  %s", glyph(a.Status), clean(a.Name), faint.Render(string(a.Status))))
		}
	}
	if n == 0 {
		lines = append(lines, faint.Render("  none"))
	}
	if ok && strings.TrimSpace(c.detail.Description) != "" {
		lines = append(lines, "", sect.Render("description"))
		for _, l := range strings.Split(strings.TrimSpace(mentionTag.ReplaceAllString(c.detail.Description, "$2")), "\n") {
			lines = append(lines, "  "+clean(l))
		}
	}
	lines = append(lines, "")
	scroll := 0
	if m.modal != nil && m.modal.id == cardID(i.Key) {
		scroll = m.modal.scroll
	}
	acts := []action{
		{"o", "open it in the browser", func() (tea.Cmd, bool) { return m.openURL(i.URL), false }},
		{"p", "its pull requests", func() (tea.Cmd, bool) { m.modal = nil; return m.pullRequests(), true }},
	}
	if m.opt.Tmux != nil {
		acts = append(acts, action{"n", "start an agent on it", func() (tea.Cmd, bool) { m.modal = nil; return m.newAgent(), true }})
	}
	m.modal = &modal{id: cardID(i.Key), title: i.Key + "  " + clean(i.Title), lines: lines, actions: acts,
		scroll: scroll, hint: "↑↓ scroll · esc close"}
}

// cardDetails refills an open card when its ticket's details arrive.
func (m *Model) cardDetails(key string) {
	if m.modal == nil || m.modal.id != cardID(key) {
		return
	}
	for _, i := range m.issues {
		if i.Key == key {
			m.showCard(i)
			return
		}
	}
}
