package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/sessions"
	"github.com/david-cik/lanes/internal/state"
)

// askSystem frames the headless call: it only matches; it has no tools and changes nothing.
const askSystem = `You help someone using lanes, a terminal board of tickets and the Claude Code
sessions working on them. You get their request; "selected_ticket", the ticket their
cursor was on when they asked (when they say "this ticket", they mean it); their open
tickets; and their recent Claude sessions (title, folder, first prompt, recent prompts,
ticket keys the session mentions, and its current ticket). Reply with only one JSON
object, no prose, in one of these shapes:
{"links":[{"session":"<session id>","ticket":"<TICKET KEY, or - to take the session off its ticket>","reason":"<one short sentence>"}]}
  — to put one or more sessions on tickets (or take them off);
{"question":"<what you need to know>"} — when the request is ambiguous;
{"answer":"<a short plain-text answer>"} — when they asked for information, not a change.
"conversation" holds the questions you already asked and their answers; use them, and
don't ask the same thing again.`

// askLink is one change Claude proposes: put a session on a ticket ("-" takes it off).
type askLink struct {
	Session, Ticket, Reason string
}

type askTicket struct{ Key, Title, State string }

type askMsg struct {
	links            []askLink
	question, answer string
	err              error
	known            map[string]string // session ids Claude was shown → title
	current          map[string]string // … → the ticket each is on now
	request          string            // what was asked, to carry on after a question
	convo            []askTurn
	selected         *askTicket // the ticket under the cursor when you asked
}

// askTurn is one follow-up: what Claude asked and what you answered.
type askTurn struct {
	Asked    string `json:"claude_asked"`
	Answered string `json:"you_answered"`
}

// ask is ":": say what you want in your own words; Claude proposes the links (or asks
// back, or answers) and you confirm any change. The ticket under the cursor goes along.
func (m *Model) ask() tea.Cmd {
	var sel *askTicket
	title := "Ask Claude — e.g. \"attach my CSP rollout session to ABC-12\""
	if i, ok := m.selectedIssue(); ok {
		sel = &askTicket{i.Key, i.Title, i.State}
		title = "Ask Claude about " + i.Key + " — e.g. \"find my old sessions for this ticket\""
	}
	m.modal = &modal{input: true, title: title,
		onInput: func(text string) tea.Cmd {
			if strings.TrimSpace(text) == "" {
				return nil
			}
			m.say("asking Claude…", nil)
			return m.askClaude(text, nil, sel)
		}}
	return nil
}

func (m *Model) askClaude(request string, convo []askTurn, sel *askTicket) tea.Cmd {
	var running []agent.Agent
	for _, a := range m.agents {
		if !a.Ended {
			running = append(running, a)
		}
	}
	var tickets []askTicket
	for _, i := range m.issues {
		tickets = append(tickets, askTicket{i.Key, i.Title, i.State})
	}
	adopted := map[string]bool{} // originals a lanes agent was forked from: the same work
	for _, r := range m.live {
		if r.AdoptedFrom != "" {
			adopted[r.AdoptedFrom] = true
		}
	}
	store, run := m.opt.Store, m.readers.Ask
	return func() tea.Msg {
		list := slices.DeleteFunc(sessions.List(running, 30, store), func(s sessions.Session) bool { return adopted[s.ID] })
		if sel != nil { // sessions about the selected ticket first, so they survive the cut
			slices.SortStableFunc(list, func(a, b sessions.Session) int {
				return cmpBool(about(b, sel.Key), about(a, sel.Key))
			})
		}
		list = list[:min(len(list), 60)]
		data, _ := json.Marshal(map[string]any{"request": request, "selected_ticket": sel, "conversation": convo,
			"open_tickets": tickets, "sessions": list})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := run(ctx, askSystem, string(data))
		if err != nil {
			return askMsg{err: err}
		}
		r := parseAsk(out)
		r.request, r.convo, r.selected = request, convo, sel
		r.known, r.current = map[string]string{}, map[string]string{}
		for _, s := range list {
			r.known[s.ID], r.current[s.ID] = s.Title, s.Ticket
		}
		return r
	}
}

// about reports whether a session is on, or mentions, ticket key.
func about(s sessions.Session, key string) bool {
	if s.Ticket == key {
		return true
	}
	for _, mn := range s.Mentions {
		if strings.HasPrefix(mn, key+" ") {
			return true
		}
	}
	return false
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

var jsonObj = regexp.MustCompile(`(?s)\{.*\}`)

func parseAsk(out string) askMsg {
	var r struct {
		Links                   []askLink
		Session, Ticket, Reason string // a single link, as older answers gave it
		Question, Answer        string
	}
	if err := json.Unmarshal([]byte(jsonObj.FindString(out)), &r); err != nil {
		return askMsg{err: fmt.Errorf("Claude's answer wasn't understood: %s", firstLine(out))}
	}
	if r.Session != "" {
		r.Links = append(r.Links, askLink{r.Session, r.Ticket, r.Reason})
	}
	return askMsg{links: r.Links, question: r.Question, answer: r.Answer}
}

func (m *Model) gotAsk(msg askMsg) tea.Cmd {
	m.notice = ""
	switch {
	case msg.err != nil:
		m.say("", msg.err)
		return nil
	case msg.question != "":
		lines := []string{faint.Render("you: " + msg.request)}
		for _, t := range msg.convo {
			lines = append(lines, faint.Render("Claude: "+t.Asked), faint.Render("you: "+t.Answered))
		}
		m.modal = &modal{input: true, title: "Claude asks: " + msg.question, lines: append(lines, ""),
			hint: "enter answer · esc stop",
			onInput: func(answer string) tea.Cmd {
				if strings.TrimSpace(answer) == "" {
					return nil
				}
				m.say("asking Claude…", nil)
				return m.askClaude(msg.request, append(slices.Clone(msg.convo), askTurn{msg.question, answer}), msg.selected)
			}}
		return nil
	case msg.answer != "" && len(msg.links) == 0:
		closeIt := func() (tea.Cmd, bool) { return nil, false }
		m.modal = &modal{title: "Claude", lines: []string{faint.Render("you: " + msg.request), "", msg.answer, ""},
			actions: []action{{"enter", "close", closeIt}}}
		return nil
	}
	// The answer is untrusted: only sessions we showed it, and well-formed tickets.
	type change struct{ id, name, ticket, reason string }
	var todo []change
	var skipped []string
	already := 0
	for _, l := range msg.links {
		name, ok := msg.known[l.Session]
		if !ok || l.Ticket == "" {
			skipped = append(skipped, l.Session)
			continue
		}
		if msg.current[l.Session] == l.Ticket || slices.ContainsFunc(todo, func(c change) bool { return c.id == l.Session }) {
			already++ // nothing to change
			continue
		}
		if name == "" {
			name = l.Session[:min(8, len(l.Session))]
		}
		for _, a := range m.agents {
			if a.ID == l.Session && a.Name != "" {
				name = a.Name
			}
		}
		todo = append(todo, change{l.Session, name, l.Ticket, l.Reason})
	}
	switch {
	case len(todo) == 0 && already > 0:
		m.say(fmt.Sprintf("nothing to change: the %d session(s) Claude found are already there", already), nil)
		return nil
	case len(todo) == 0 && len(skipped) > 0:
		m.say("", fmt.Errorf("Claude named session %q, which isn't one of your recent sessions", skipped[0]))
		return nil
	case len(todo) == 0:
		m.say("", fmt.Errorf("Claude didn't name a session and a ticket"))
		return nil
	}
	verb := func(c change) string {
		if c.ticket == state.NoTicket {
			return fmt.Sprintf("Take %s off its ticket", c.name)
		}
		return fmt.Sprintf("Link %s to %s", c.name, c.ticket)
	}
	var title string
	var lines []string
	if len(todo) == 1 {
		title = verb(todo[0]) + "?"
		lines = []string{"session " + todo[0].id, "", todo[0].reason}
	} else {
		title = fmt.Sprintf("Make these %d changes?", len(todo))
		for _, c := range todo {
			lines = append(lines, "• "+verb(c)+faint.Render(" ("+c.id[:min(8, len(c.id))]+")"), faint.Render("  "+c.reason))
		}
	}
	if already > 0 {
		lines = append(lines, "", faint.Render(fmt.Sprintf("(%d more already where Claude would put them)", already)))
	}
	if len(skipped) > 0 {
		lines = append(lines, faint.Render(fmt.Sprintf("(left out %d session(s) that aren't among your recent ones)", len(skipped))))
	}
	store := m.opt.Store
	m.modal = &modal{confirm: true, title: title, lines: lines,
		onYes: func() tea.Cmd {
			var errs []error
			for _, c := range todo {
				errs = append(errs, sessions.Link(context.Background(), store, c.id, c.ticket))
			}
			m.links, _ = store.Links()
			m.rebuild()
			done := strings.TrimSuffix(title, "?") + " — done"
			if len(todo) > 1 {
				done = fmt.Sprintf("made %d changes", len(todo))
			}
			m.say(done, errors.Join(errs...))
			return m.fetchAgents()
		}}
	return nil
}

// claudeAsk runs a one-shot Claude with no tools, no saved session, and none of the
// user's settings (so no hooks fire), and returns its text answer.
func claudeAsk(model string) func(ctx context.Context, system, prompt string) (string, error) {
	return func(ctx context.Context, system, prompt string) (string, error) {
		argv := []string{"-p", "--tools", "", "--no-session-persistence", "--setting-sources", "",
			"--output-format", "json", "--system-prompt", system}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		cmd := exec.CommandContext(ctx, "claude", append(argv, prompt)...)
		cmd.Env = claude.CleanEnv(os.Environ())
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("claude: %w", err)
		}
		var r struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal(out, &r) != nil {
			return string(out), nil
		}
		if r.IsError {
			return "", fmt.Errorf("claude: %s", firstLine(r.Result))
		}
		return r.Result, nil
	}
}
