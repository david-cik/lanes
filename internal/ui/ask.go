package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
sessions working on them. You get their request, their open tickets, and their recent
Claude sessions (title, folder, first prompt, recent prompts, ticket keys the session
mentions, and its current ticket). Pick the one session and the one ticket the request
means. Reply with only a JSON object, no prose:
{"session":"<session id>","ticket":"<TICKET KEY, or - to take the session off its ticket>","reason":"<one short sentence>"}
If the request is ambiguous or nothing fits, reply {"question":"<what you need to know>"}.`

type askMsg struct {
	session, ticket, reason, question string
	err                               error
	known                             map[string]string // session ids Claude was shown → title
}

// ask is "/": describe the session and the ticket in your own words; Claude proposes
// the link and you confirm it.
func (m *Model) ask() tea.Cmd {
	m.modal = &modal{input: true, title: "Ask Claude — e.g. \"attach my CSP rollout session to ABC-12\"",
		onInput: func(text string) tea.Cmd {
			if strings.TrimSpace(text) == "" {
				return nil
			}
			m.say("asking Claude…", nil)
			return m.askClaude(text)
		}}
	return nil
}

func (m *Model) askClaude(request string) tea.Cmd {
	var running []agent.Agent
	for _, a := range m.agents {
		if !a.Ended {
			running = append(running, a)
		}
	}
	type ticket struct{ Key, Title, State string }
	var tickets []ticket
	for _, i := range m.issues {
		tickets = append(tickets, ticket{i.Key, i.Title, i.State})
	}
	store, run := m.opt.Store, m.readers.Ask
	return func() tea.Msg {
		list := sessions.List(running, 7, store)
		if len(list) > 40 {
			list = list[:40]
		}
		data, _ := json.Marshal(map[string]any{"request": request, "open_tickets": tickets, "sessions": list})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := run(ctx, askSystem, string(data))
		if err != nil {
			return askMsg{err: err}
		}
		r := parseAsk(out)
		r.known = map[string]string{}
		for _, s := range list {
			r.known[s.ID] = s.Title
		}
		return r
	}
}

var jsonObj = regexp.MustCompile(`(?s)\{.*\}`)

func parseAsk(out string) askMsg {
	var r struct {
		Session, Ticket, Reason, Question string
	}
	if err := json.Unmarshal([]byte(jsonObj.FindString(out)), &r); err != nil {
		return askMsg{err: fmt.Errorf("Claude's answer wasn't understood: %s", firstLine(out))}
	}
	return askMsg{session: r.Session, ticket: r.Ticket, reason: r.Reason, question: r.Question}
}

func (m *Model) gotAsk(msg askMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.say("", msg.err)
		return nil
	case msg.question != "":
		m.say("Claude asks: "+msg.question, nil)
		return nil
	case msg.session == "" || msg.ticket == "":
		m.say("", fmt.Errorf("Claude didn't name a session and a ticket"))
		return nil
	}
	name, ok := msg.known[msg.session]
	if !ok { // the answer is untrusted: only sessions we showed it
		m.say("", fmt.Errorf("Claude named session %q, which isn't one of your recent sessions", msg.session))
		return nil
	}
	if name == "" {
		name = msg.session[:min(8, len(msg.session))]
	}
	for _, a := range m.agents {
		if a.ID == msg.session && a.Name != "" {
			name = a.Name
		}
	}
	title := fmt.Sprintf("Link %s to %s?", name, msg.ticket)
	if msg.ticket == state.NoTicket {
		title = fmt.Sprintf("Take %s off its ticket?", name)
	}
	store := m.opt.Store
	m.notice = ""
	m.modal = &modal{confirm: true, title: title, lines: []string{"session " + msg.session, "", msg.reason},
		onYes: func() tea.Cmd {
			err := sessions.Link(context.Background(), store, msg.session, msg.ticket)
			if err == nil {
				m.links, _ = store.Links()
				m.rebuild()
			}
			m.say(title[:len(title)-1]+" — done", err)
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
