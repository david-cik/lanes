package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/tracker"
)

// rowY is the screen line of the first board row containing text.
func rowY(t *testing.T, m *Model, text string) int {
	t.Helper()
	for i, l := range strings.Split(view(m), "\n") {
		if strings.Contains(l, text) {
			return i
		}
	}
	t.Fatalf("no line containing %q:\n%s", text, view(m))
	return 0
}

func click(m *Model, x, y int) { m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) }

func TestMovingShowsAgentWithoutFocus(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("j")) // onto agent "one"
	if strings.Join(ft.log, "|") != "swap %1 %P" {
		t.Fatalf("tmux calls %v", ft.log)
	}
	m.Update(key("l"))
	if ft.log[len(ft.log)-1] != "select %1" {
		t.Fatalf("l didn't focus the agent: %v", ft.log)
	}
}

func TestClickSelectsThenOpens(t *testing.T) {
	m, ft := controlModel(t)
	y := rowY(t, m, "two")
	click(m, 5, y)
	if r, _ := m.selected(); r.Agent == nil || r.Agent.Name != "two" {
		t.Fatalf("selected %+v", r)
	}
	if strings.Contains(strings.Join(ft.log, "|"), "select") {
		t.Fatalf("first click focused: %v", ft.log)
	}
	click(m, 5, y)
	if ft.log[len(ft.log)-1] != "select %2" {
		t.Fatalf("second click didn't open: %v", ft.log)
	}
}

func TestWheelMovesAndFooterClickPresses(t *testing.T) {
	m, _ := controlModel(t)
	m.cursor = 0
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 2 {
		t.Fatalf("cursor %d", m.cursor)
	}
	footer := strings.Split(view(m), "\n")[m.height-1]
	x := strings.Index(footer, "⟨?⟩ keys")
	if x < 0 {
		t.Fatalf("footer %q", footer)
	}
	click(m, len([]rune(footer[:x])), m.height-1)
	if m.modal == nil || m.modal.title != "Keys" {
		t.Fatalf("footer click didn't open help: %+v", m.modal)
	}
}

func TestClickModalChoiceAndConfirm(t *testing.T) {
	m, _ := controlModel(t)
	m.cursorTo(t, "outside")
	m.Update(key("t"))
	click(m, 4, rowY(t, m, "ABC-1"))
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-1" {
		t.Fatalf("links %v", l)
	}
	m.cursorTo(t, "one")
	m.Update(key("x"))
	click(m, 3, rowY(t, m, "  n    no"))
	if m.modal != nil {
		t.Fatal("clicking no didn't close the confirm")
	}
}

func TestFilter(t *testing.T) {
	m, _ := controlModel(t)
	for _, k := range []string{"/", "o", "u", "t"} {
		m.Update(key(k))
	}
	v := view(m)
	if !strings.Contains(v, "outside") || strings.Contains(v, "ABC-1 fix it") || !strings.Contains(v, "/out") {
		t.Fatalf("filtered view:\n%s", v)
	}
	m.Update(key("enter"))
	m.Update(key("j")) // navigates again, not typed
	if m.filter != "out" {
		t.Fatalf("filter %q", m.filter)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.filter != "" || !strings.Contains(view(m), "ABC-1 fix it") {
		t.Fatalf("esc didn't clear:\n%s", view(m))
	}
	m.Update(key("/"))
	for _, k := range []string{"f", "i", "x"} { // ticket title: its agents stay too
		m.Update(key(k))
	}
	if v := view(m); !strings.Contains(v, "one") || strings.Contains(v, "outside") {
		t.Fatalf("ticket filter:\n%s", v)
	}
}

func TestClickBelowShortListPicksNothing(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 6}) // room for two items
	picked := ""
	m.modal = &modal{title: "pick", items: []choice{{"first", "1"}, {"second", "2"}, {"third", "3"}},
		onChoose: func(c choice) tea.Cmd { picked = c.value; return nil }}
	click(m, 2, m.height-1) // the footer, just below the visible items
	if picked != "" {
		t.Fatalf("picked %q from off screen", picked)
	}
	click(m, 2, rowY(t, m, "first"))
	if picked != "1" {
		t.Fatalf("picked %q", picked)
	}
}

func TestAdoptedOriginalIsHidden(t *testing.T) {
	m, _ := controlModel(t)
	snap := m.opt.Fleet.(*fleetSnap)
	snap.live[0].AdoptedFrom = "x9" // agent "one" was forked from the external "outside"
	m.Update(agentsMsg{seq: m.agentSeq + 1, agents: snap.agents, live: snap.live})
	if v := view(m); strings.Contains(v, "outside") || !strings.Contains(v, "one") {
		t.Fatalf("adopted original still shown:\n%s", v)
	}
}

func TestModalTextWrapsAndClicksStillLandOnItems(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	picked := ""
	m.modal = &modal{title: "a long title that cannot fit in thirty columns at all",
		lines:    []string{"and a reason that also runs well past the panel's edge"},
		items:    []choice{{"first", "1"}, {"second", "2"}},
		onChoose: func(c choice) tea.Cmd { picked = c.value; return nil }}
	v := view(m)
	if !strings.Contains(v, "at all") || !strings.Contains(v, "edge") {
		t.Fatalf("not wrapped:\n%s", v)
	}
	click(m, 4, rowY(t, m, "second"))
	if picked != "2" {
		t.Fatalf("picked %q", picked)
	}
}

func TestAskKnowsTheSelectedTicketAndProposesSeveralLinks(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	endedTranscript(t, cfg, "x9", "/src", "outside work")
	endedTranscript(t, cfg, "y8", "/src", "older work")
	m, _ := controlModel(t)
	var prompt string
	m.readers.Ask = func(_ context.Context, _, p string) (string, error) {
		prompt = p
		return `{"links":[{"session":"x9","ticket":"ABC-1","reason":"same bug"},{"session":"y8","ticket":"ABC-1","reason":"earlier try"},{"session":"y8","ticket":"ABC-1","reason":"again"}]}`, nil
	}
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key(":"))
	if !strings.Contains(m.modal.title, "about ABC-1") {
		t.Fatalf("title %q", m.modal.title)
	}
	for _, r := range "find my old sessions for this ticket" {
		m.Update(key(string(r)))
	}
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if !strings.Contains(prompt, `"selected_ticket":{"Key":"ABC-1"`) {
		t.Fatalf("prompt lacks the selected ticket: %s", prompt)
	}
	if m.modal == nil || m.modal.title != "Make these 2 changes?" || !strings.Contains(strings.Join(m.modal.lines, " "), "1 more already") {
		t.Fatalf("confirm %+v", m.modal)
	}
	m.Update(key("y"))
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-1" || l["claude:y8"] != "ABC-1" {
		t.Fatalf("links %v", l)
	}
}

func TestAskInformationalAnswer(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(askMsg{answer: "You have no sessions about ABC-1 yet.", request: "any sessions?"})
	if m.modal == nil || !strings.Contains(strings.Join(m.modal.lines, " "), "no sessions about ABC-1") {
		t.Fatalf("answer %+v", m.modal)
	}
	m.Update(key("enter"))
	if m.modal != nil {
		t.Fatal("enter didn't close the answer")
	}
}

func TestAskNothingToChange(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(askMsg{links: []askLink{{"x9", "ABC-1", "it's there"}}, known: map[string]string{"x9": "outside"},
		current: map[string]string{"x9": "ABC-1"}})
	if m.modal != nil || !strings.Contains(m.notice, "already there") {
		t.Fatalf("modal %+v notice %q", m.modal, m.notice)
	}
}

func TestWrapKeepsIndent(t *testing.T) {
	got := wrap("  a reason long enough to need a second line here", 24)
	if len(got) < 2 || !strings.HasPrefix(got[1], "  ") {
		t.Fatalf("%q", got)
	}
}

func TestProjectTree(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.Config.GroupByProject = true
	m.issues = []tracker.Issue{
		{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted", Project: "Launch"},
		{Key: "ABC-2", Title: "ship it", Team: "Alpha", State: "Todo", StateType: "unstarted", Project: "Launch"},
	}
	m.rebuild()
	v := view(m)
	for _, want := range [][]string{{"◆ Launch", "· 2"}, {"├─ ABC-1 fix it"}, {"│", "├─", "one"}, {"└─ ABC-2 ship it"}} {
		if !rowHas(v, want...) {
			t.Fatalf("no row with %q:\n%s", want, v)
		}
	}
}

func TestFoldTeamKeepsWaitingVisible(t *testing.T) {
	m, _ := controlModel(t)
	m.issues = append(m.issues, tracker.Issue{Key: "XYZ-1", Title: "other team", Team: "Beta", State: "Todo", StateType: "unstarted"})
	m.hooks["r1"] = &hookState{status: agent.Waiting}
	m.rebuild()
	m.cursorTo(t, "Alpha")
	m.Update(key("enter"))
	v := view(m)
	if strings.Contains(v, "ABC-1 fix it") || !rowHas(v, "▸ Alpha", "1", "⚠ 1") || !strings.Contains(v, "XYZ-1 other team") {
		t.Fatalf("folded Alpha:\n%s", v)
	}
	if r, _ := m.selected(); r.Kind != board.TeamRow || r.Text != "Alpha" {
		t.Fatalf("cursor left the header: %+v", r)
	}
	m.Update(key("]"))
	if r, _ := m.selected(); r.Kind != board.TeamRow || r.Text != "Beta" {
		t.Fatalf("] went to %+v", r)
	}
	m.Update(key("["))
	m.Update(key("enter"))
	if !strings.Contains(view(m), "ABC-1 fix it") {
		t.Fatalf("didn't unfold:\n%s", view(m))
	}
}

func TestSelectionFollowsAMovedTicket(t *testing.T) {
	m, _ := controlModel(t)
	m.issues = []tracker.Issue{
		{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted"},
		{Key: "ABC-2", Title: "ship it", Team: "Alpha", State: "Todo", StateType: "unstarted"},
	}
	m.rebuild()
	m.cursorTo(t, "ABC-2 ship it")
	m.Update(issuesMsg{assignee: "me", issues: []tracker.Issue{ // ABC-2 moves ahead to Doing
		{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted"},
		{Key: "ABC-2", Title: "ship it", Team: "Alpha", State: "Doing", StateType: "started"},
	}})
	if r, _ := m.selected(); r.Kind != board.TicketRow || r.Issue.Key != "ABC-2" {
		t.Fatalf("selection stayed behind on %+v", r)
	}
}
