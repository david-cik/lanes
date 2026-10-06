package ui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/gh"
	"github.com/david-cik/lanes/internal/gitinfo"
	"github.com/david-cik/lanes/internal/tracker"
)

const (
	detailDelay = 300 * time.Millisecond
	gitTTL      = 5 * time.Second
	prTTL       = 30 * time.Second
)

type (
	detailTick struct{ seq int }
	gitMsg     struct {
		dir  string
		info gitinfo.Info
		err  error
	}
	prMsg struct {
		key string // dir + "\x00" + branch
		pr  *gh.PR
		err error
	}
	issueMsg struct {
		key    string
		detail tracker.IssueDetail
		err    error
	}
	cachedIssue struct {
		detail tracker.IssueDetail
		err    error
	}
)

type cachedGit struct {
	info gitinfo.Info
	err  error
	at   time.Time
}

type cachedPR struct {
	pr  *gh.PR
	err error
	at  time.Time
}

// Readers are how the details view gets its facts; tests replace them.
type Readers struct {
	Git  func(ctx context.Context, dir string) (gitinfo.Info, error)
	PR   func(ctx context.Context, dir, branch string) (*gh.PR, error)
	Open func(url string) error
}

func defaultReaders() Readers {
	return Readers{
		Git: gitinfo.Read,
		PR: func(ctx context.Context, dir, branch string) (*gh.PR, error) {
			return gh.Find(ctx, gh.Exec, dir, branch)
		},
		Open: func(url string) error {
			if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
				return fmt.Errorf("not opening %q: not a web address", url)
			}
			name := "xdg-open"
			if runtime.GOOS == "darwin" {
				name = "open"
			}
			cmd := exec.Command(name, url)
			if err := cmd.Start(); err != nil {
				return err
			}
			go cmd.Wait() // reap it
			return nil
		},
	}
}

// detailsMoved is called after anything that may change the selected row.
func (m *Model) detailsMoved() tea.Cmd {
	if !m.details {
		return nil
	}
	m.detailSeq++
	seq := m.detailSeq
	return tea.Tick(detailDelay, func(time.Time) tea.Msg { return detailTick{seq} })
}

// fetchDetails loads whatever the selected row needs that isn't cached and fresh.
func (m *Model) fetchDetails(force bool) tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	now := m.now()
	rd := m.readers
	var cmds []tea.Cmd
	switch {
	case r.Kind == board.AgentRow && r.Agent.Cwd != "":
		dir, branch := r.Agent.Cwd, r.Agent.Branch
		if c, ok := m.gitCache[dir]; force || !ok || now.Sub(c.at) > gitTTL {
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				in, err := rd.Git(ctx, dir)
				return gitMsg{dir, in, err}
			})
		}
		key := dir + "\x00" + branch
		if c, ok := m.prCache[key]; force || !ok || now.Sub(c.at) > prTTL {
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				pr, err := rd.PR(ctx, dir, branch)
				return prMsg{key, pr, err}
			})
		}
	case r.Kind == board.TicketRow:
		key := r.Issue.Key
		if _, ok := m.issueDetails[key]; force || !ok {
			tr := m.opt.Tracker
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				d, err := tr.Issue(ctx, key)
				return issueMsg{key, d, err}
			})
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) detailsUpdate(msg tea.Msg) {
	now := m.now()
	switch msg := msg.(type) {
	case gitMsg:
		m.gitCache[msg.dir] = cachedGit{msg.info, msg.err, now}
	case prMsg:
		m.prCache[msg.key] = cachedPR{msg.pr, msg.err, now}
	case issueMsg:
		m.issueDetails[msg.key] = cachedIssue{msg.detail, msg.err}
	}
}

// openSelected opens the selected agent's PR or the selected ticket in the browser.
func (m *Model) openSelected() tea.Cmd {
	r, ok := m.selected()
	url := ""
	switch {
	case !ok:
	case r.Kind == board.TicketRow:
		url = r.Issue.URL
	case r.Kind == board.AgentRow:
		if c, ok := m.prCache[r.Agent.Cwd+"\x00"+r.Agent.Branch]; ok && c.pr != nil {
			url = c.pr.URL
		}
	}
	if url == "" {
		m.say("", fmt.Errorf("nothing to open here (agents open their PR, tickets open in the tracker)"))
		return nil
	}
	open := m.readers.Open
	return func() tea.Msg {
		if err := open(url); err != nil {
			return noticeMsg{"", err}
		}
		return noticeMsg{"opened " + url, nil}
	}
}

var (
	okC   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	badC  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	pendC = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	sect  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

func (m *Model) viewDetails(b *strings.Builder, height int) {
	var lines []string
	r, ok := m.selected()
	switch {
	case !ok:
		lines = []string{faint.Render("nothing selected")}
	case r.Kind == board.AgentRow:
		lines = m.agentDetails(r.Agent)
	case r.Kind == board.TicketRow:
		lines = m.ticketDetails(r.Issue)
	default:
		lines = []string{faint.Render("select an agent or a ticket")}
	}
	for i := 0; i < height; i++ {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteString("\n")
	}
}

// fit cleans text from outside (tracker, gh, git, agents) and cuts it to the panel width.
// Call it on plain text, before styling: trunc counts style escapes as characters.
func (m *Model) fit(s string) string { return trunc(clean(s), m.width) }

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}

func (m *Model) agentDetails(a *agent.Agent) []string {
	out := []string{bold.Render(m.fit(fmt.Sprintf("%s %s %s · %s %s", glyph(a.Status), a.Tool, a.Name, a.Status, age(m.now().Sub(a.Since)))))}
	if note := m.waitingNote(a); note != "" {
		out = append(out, waitS.Render(m.fit("  waiting"+note+"  (a to answer)")))
	}

	out = append(out, sect.Render("git"))
	switch c, ok := m.gitCache[a.Cwd]; {
	case a.Cwd == "":
		out = append(out, faint.Render("  no working directory known"))
	case !ok:
		out = append(out, faint.Render("  loading…"))
	case c.err != nil:
		out = append(out, faint.Render(m.fit("  "+firstLine(c.err.Error()))))
	default:
		in := c.info
		br := in.Branch
		if br == "" {
			br = "(detached)"
		}
		line := "  " + br
		if in.Upstream != "" {
			line += fmt.Sprintf(" · ↑%d ↓%d vs %s", in.UpAhead, in.UpBehind, in.Upstream)
		}
		out = append(out, m.fit(line))
		line = "  "
		if in.Base != "" {
			line += fmt.Sprintf("↑%d ↓%d vs %s · ", in.BaseAhead, in.BaseBehind, in.Base)
		}
		if in.Dirty > 0 {
			out = append(out, m.fit(line)+pendC.Render(m.fitAfter(line, fmt.Sprintf("%d uncommitted", in.Dirty))))
		} else {
			out = append(out, m.fit(line+"clean"))
		}
		if in.Subject != "" {
			out = append(out, faint.Render(m.fit(fmt.Sprintf("  last: %s (%s)", in.Subject, ago(m.now().Sub(in.When))))))
		}
	}

	out = append(out, sect.Render("pull request"))
	switch c, ok := m.prCache[a.Cwd+"\x00"+a.Branch]; {
	case a.Branch == "":
		out = append(out, faint.Render("  no branch"))
	case !ok:
		out = append(out, faint.Render("  loading…"))
	case c.err != nil:
		out = append(out, faint.Render(m.fit("  "+firstLine(c.err.Error()))))
	case c.pr == nil:
		out = append(out, faint.Render(m.fit("  no PR for "+a.Branch)))
	default:
		out = append(out, prLines(c.pr, m)...)
	}

	out = append(out, sect.Render("recent"))
	st := m.hooks[hookKey(a)]
	switch {
	case a.RecordID == "" && st == nil:
		out = append(out, faint.Render("  not started by lanes — no activity log (lanes install-hooks adds one)"))
	case st == nil || len(st.activity) == 0:
		out = append(out, faint.Render("  nothing yet"))
	default:
		for i := len(st.activity) - 1; i >= 0; i-- {
			e := st.activity[i]
			stamp := "  " + e.at.Format("15:04") + " "
			out = append(out, faint.Render(stamp)+m.fitAfter(stamp, e.text))
		}
	}
	return out
}

// prLines renders a PR in two short coloured lines plus the URL. The coloured parts
// are tiny and fixed; the only free text (failing check names) is cut to what's left.
func prLines(pr *gh.PR, m *Model) []string {
	state := strings.ToLower(pr.State)
	if pr.Draft {
		state = "draft"
	}
	head := fmt.Sprintf("  #%d %s", pr.Number, state)
	review := map[string]string{"APPROVED": okC.Render(" · approved"), "CHANGES_REQUESTED": badC.Render(" · changes requested"),
		"REVIEW_REQUIRED": pendC.Render(" · review required")}[pr.Review]
	c := pr.Checks
	plain := fmt.Sprintf("  checks ✓%d", c.Pass)
	checks := "  checks " + okC.Render(fmt.Sprintf("✓%d", c.Pass))
	if c.Fail > 0 {
		plain += fmt.Sprintf(" ✗%d", c.Fail)
		checks += " " + badC.Render(fmt.Sprintf("✗%d", c.Fail))
	}
	if c.Pending > 0 {
		plain += fmt.Sprintf(" …%d", c.Pending)
		checks += " " + pendC.Render(fmt.Sprintf("…%d", c.Pending))
	}
	if c.Skipped > 0 {
		plain += fmt.Sprintf(" −%d", c.Skipped)
		checks += faint.Render(fmt.Sprintf(" −%d", c.Skipped))
	}
	if len(pr.Failed) > 0 {
		checks += m.fitAfter(plain, " · failing: "+strings.Join(pr.Failed, ", "))
	}
	return []string{m.fit(head) + review, checks, faint.Render(m.fit("  " + pr.URL + "  (o opens)"))}
}

// fitAfter cuts s to the width left after the plain text prefix.
func (m *Model) fitAfter(prefix, s string) string {
	return trunc(clean(s), max(m.width-len([]rune(prefix)), 1))
}

// ago is age phrased for a past moment.
func ago(d time.Duration) string {
	if a := age(d); a != "now" {
		return a + " ago"
	}
	return "just now"
}

func (m *Model) ticketDetails(i *tracker.Issue) []string {
	out := []string{
		bold.Render(m.fit(i.Key + " " + i.Title)),
		m.fit("  " + i.Team + " · " + i.State),
		faint.Render(m.fit("  " + i.URL + "  (o opens)")),
	}
	c, ok := m.issueDetails[i.Key]
	d := c.detail
	out = append(out, sect.Render("pull requests"))
	switch {
	case !ok:
		out = append(out, faint.Render("  loading…"))
	case c.err != nil:
		out = append(out, faint.Render(m.fit("  "+firstLine(c.err.Error()))))
	case len(d.PRURLs) == 0:
		out = append(out, faint.Render("  none linked"))
	default:
		for _, u := range d.PRURLs {
			out = append(out, m.fit("  "+u))
		}
	}
	n := 0
	for _, a := range m.agents {
		if a.TicketKey == i.Key {
			n++
		}
	}
	out = append(out, sect.Render("agents"), fmt.Sprintf("  %d on this ticket", n))
	if ok && d.Description != "" {
		out = append(out, sect.Render("description"))
		for _, l := range strings.Split(d.Description, "\n") {
			if strings.TrimSpace(l) != "" {
				out = append(out, m.fit("  "+l))
			}
		}
	}
	return out
}
