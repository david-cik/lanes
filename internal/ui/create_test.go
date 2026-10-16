package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/tracker"
)

// creatingTracker makes tickets and then lists them.
type creatingTracker struct {
	*fakeTracker
	made []string // team|title|description|project
	list []tracker.Issue
}

func (c *creatingTracker) Issues(context.Context, string) ([]tracker.Issue, error) {
	return c.list, nil
}
func (c *creatingTracker) Create(_ context.Context, team, title, desc, project string) (string, error) {
	c.made = append(c.made, strings.Join([]string{team, title, desc, project}, "|"))
	c.list = append(c.list, tracker.Issue{Key: "ABC-9", Title: title, Team: team, State: "Todo", StateType: "unstarted", Project: project})
	return "ABC-9", nil
}

func draftModel(t *testing.T, reply string, err error) (*Model, *creatingTracker, *[]string) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	endedTranscript(t, cfg, "x9", "/src", "outside work")
	m, _ := controlModel(t)
	m.issues[0].Project = "Launch"
	m.rebuild()
	ct := &creatingTracker{fakeTracker: &fakeTracker{}, list: m.issues}
	m.opt.Tracker = ct
	var prompts []string
	m.readers.Ask = func(_ context.Context, _, p string) (string, error) { prompts = append(prompts, p); return reply, err }
	return m, ct, &prompts
}

func typeIn(m *Model, text string) {
	for _, r := range text {
		m.Update(key(string(r)))
	}
}

func TestNewTicketDraftedFromWords(t *testing.T) {
	m, ct, prompts := draftModel(t, `{"title":"Rotate the API keys","description":"Why: they leaked.\nDone when: old keys revoked."}`, nil)
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("N"))
	typeIn(m, "rotate keys")
	_, cmd := m.Update(key("enter"))
	m.Update(cmd()) // the draft
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], `"request":"rotate keys"`) || !strings.Contains((*prompts)[0], `"project":"Launch"`) {
		t.Fatalf("prompt %v", *prompts)
	}
	if v := strings.Join(m.modal.lines, " "); m.modal.title != "New ticket" || !strings.Contains(v, "Rotate the API keys") || !strings.Contains(v, "Done when") {
		t.Fatalf("review %+v", m.modal)
	}
	_, cmd = m.Update(key("y"))
	_, cmd = m.Update(cmd()) // created → refresh
	m.Update(cmd())
	if len(ct.made) != 1 || ct.made[0] != "Alpha|Rotate the API keys|Why: they leaked.\nDone when: old keys revoked.|Launch" {
		t.Fatalf("made %q", ct.made)
	}
	if r, _ := m.selected(); r.Issue == nil || r.Issue.Key != "ABC-9" {
		t.Fatalf("cursor on %+v", r)
	}
}

func TestNewTicketDraftedFromASessionPutsItOnTheTicket(t *testing.T) {
	m, ct, prompts := draftModel(t, `{"title":"Ship the outside work","description":"from the session"}`, nil)
	m.cursorTo(t, "outside")
	_, cmd := m.Update(key("N")) // straight to drafting, no words needed
	m.Update(cmd())
	if !strings.Contains((*prompts)[0], `"session":{`) || !strings.Contains((*prompts)[0], "outside work") ||
		!strings.Contains(strings.Join(m.modal.lines, " "), "then outside goes on it") {
		t.Fatalf("prompt %v modal %+v", *prompts, m.modal)
	}
	_, cmd = m.Update(key("y"))
	m.Update(cmd()) // created: then the session goes on it
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-9" || !strings.Contains(m.notice, "put outside on it") {
		t.Fatalf("links %v notice %q made %v", l, m.notice, ct.made)
	}
}

func TestNewTicketDuplicateAndFallback(t *testing.T) {
	m, _, _ := draftModel(t, `{"duplicate":"ABC-1","reason":"ABC-1 already rotates them"}`, nil)
	m.cursorTo(t, "outside")
	_, cmd := m.Update(key("N"))
	m.Update(cmd())
	if m.modal == nil || m.modal.title != "ABC-1 may already cover this" {
		t.Fatalf("duplicate %+v", m.modal)
	}
	m.Update(key("l")) // put the session on ABC-1 instead
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-1" {
		t.Fatalf("links %v", l)
	}

	m, _, prompts := draftModel(t, `{"duplicate":"ABC-1","reason":"covered"}`, nil)
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("N"))
	typeIn(m, "rotate keys")
	_, cmd = m.Update(key("enter"))
	m.Update(cmd())
	_, cmd = m.Update(key("c")) // create anyway: the same request again, told to skip the check
	cmd()
	if len(*prompts) != 2 || !strings.Contains((*prompts)[1], `"request":"rotate keys"`) || !strings.Contains((*prompts)[1], `"create_anyway":true`) {
		t.Fatalf("second draft %v", *prompts)
	}

	m, _, _ = draftModel(t, "", errors.New("claude: not signed in"))
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("N"))
	typeIn(m, "x")
	_, cmd = m.Update(key("enter"))
	m.Update(cmd())
	if m.modal == nil || !m.modal.input || !strings.Contains(m.modal.title, "title") || !strings.Contains(m.notice, "type the ticket yourself") {
		t.Fatalf("fallback %+v notice %q", m.modal, m.notice)
	}
}

func TestNewTicketFromUnlinkedAsksTheTeam(t *testing.T) {
	m, _ := controlModel(t)
	m.issues = append(m.issues, tracker.Issue{Key: "XYZ-1", Title: "other", Team: "Beta", State: "Todo", StateType: "unstarted"})
	m.opt.Tracker = &creatingTracker{fakeTracker: &fakeTracker{}}
	m.rebuild()
	m.cursorTo(t, "outside") // an agent under Unlinked
	m.Update(key("N"))
	if m.modal == nil || m.modal.title != "New ticket in which team?" {
		t.Fatalf("modal %+v", m.modal)
	}
}

func TestNewTicketUnknownDuplicateFallsBack(t *testing.T) {
	m, _, _ := draftModel(t, `{"duplicate":"ABC-77"}`, nil) // not on the board
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("N"))
	typeIn(m, "x")
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.modal == nil || !m.modal.input || !strings.Contains(m.modal.title, "title") {
		t.Fatalf("an empty draft reached review: %+v", m.modal)
	}
}
