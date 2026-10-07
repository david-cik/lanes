package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/tracker"
)

type fakeTracker struct{ calls []string }

func (f *fakeTracker) Issues(_ context.Context, who string) ([]tracker.Issue, error) {
	f.calls = append(f.calls, who)
	return nil, nil
}
func (f *fakeTracker) Issue(_ context.Context, key string) (tracker.IssueDetail, error) {
	return tracker.IssueDetail{Issue: tracker.Issue{Key: key}}, nil
}
func (f *fakeTracker) Users(context.Context) ([]tracker.User, error) {
	return []tracker.User{{ID: "u1", Name: "Ada"}, {ID: "u2", Name: "Grace"}}, nil
}

func newModel() (*Model, *fakeTracker) {
	ft := &fakeTracker{}
	m := New(Options{
		Tracker: ft, Assignee: "me", LinearPoll: time.Minute, ExternalPoll: time.Second,
		Issues: []tracker.Issue{{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "In Progress", StateType: "started"}},
	})
	m.now = func() time.Time { return time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC) }
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return m, ft
}

// view is the screen as plain text (styles stripped), what a user reads.
func view(m *Model) string { return ansiRe.ReplaceAllString(m.View().Content, "") }

func TestAgentsNestUnderTicket(t *testing.T) {
	m, _ := newModel()
	m.Update(agentsMsg{agents: []agent.Agent{
		{Tool: "claude", Name: "impl", Branch: "abc-1-fix-it", Status: agent.Working, External: true,
			Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}})
	v := view(m)
	ticket, ag := strings.Index(v, "ABC-1 fix it"), strings.Index(v, "└ ● impl  working 1h · abc-1-fix-it  external")
	if ticket < 0 || ag < ticket {
		t.Fatalf("agent not under ticket:\n%s", v)
	}
}

func TestRefreshErrorKeepsData(t *testing.T) {
	m, _ := newModel()
	m.Update(issuesMsg{assignee: "me", err: errors.New("network down\nmore detail")})
	v := view(m)
	if !strings.Contains(v, "ABC-1 fix it") || !strings.Contains(v, "tracker: network down") || strings.Contains(v, "more detail") {
		t.Fatalf("view:\n%s", v)
	}
	m.Update(issuesMsg{assignee: "me"})
	if strings.Contains(view(m), "network down") {
		t.Fatal("error not cleared after a good refresh")
	}
}

func key(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	panic(s)
}

func TestAssigneePicker(t *testing.T) {
	m, ft := newModel()
	_, cmd := m.Update(key("u"))
	m.Update(cmd()) // usersMsg
	for _, k := range []string{"g", "r", "a"} {
		m.Update(key(k))
	}
	if v := view(m); !strings.Contains(v, "Grace") || strings.Contains(v, "Ada") {
		t.Fatalf("filter not applied:\n%s", v)
	}
	m.Update(key("down")) // past "me" to Grace
	_, cmd = m.Update(key("enter"))
	cmd()
	if len(ft.calls) != 1 || ft.calls[0] != "u2" || !strings.Contains(view(m), "  Grace · ") {
		t.Fatalf("calls=%v view:\n%s", ft.calls, view(m))
	}
}

func TestStaleAssigneeResultDropped(t *testing.T) {
	m, _ := newModel()
	m.Update(issuesMsg{assignee: "someone-else"})
	if !strings.Contains(view(m), "ABC-1 fix it") {
		t.Fatal("stale result replaced current tickets")
	}
}

func TestOlderAgentRefreshIgnored(t *testing.T) {
	m, _ := newModel()
	m.agentSeq = 2
	m.Update(agentsMsg{seq: 2, agents: []agent.Agent{{Tool: "claude", Name: "new", Branch: "abc-1-x"}}})
	m.Update(agentsMsg{seq: 1, agents: []agent.Agent{{Tool: "claude", Name: "old", Branch: "abc-1-x"}}})
	if v := view(m); !strings.Contains(v, "new") || strings.Contains(v, "old") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestSlowRefreshStillAppliesWhileNewerOnesRun(t *testing.T) {
	m, _ := newModel()
	m.agentSeq = 5 // refreshes 4 and 5 started after 3, neither has returned
	m.Update(agentsMsg{seq: 3, agents: []agent.Agent{{Tool: "claude", Name: "late", Branch: "abc-1-x"}}})
	if !strings.Contains(view(m), "late") {
		t.Fatal("a late refresh was dropped although nothing newer had landed")
	}
}

func TestCtrlCQuitsFromModal(t *testing.T) {
	m, _ := newModel()
	m.Update(key("u"))
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c in a modal did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c in a modal did not quit")
	}
}

func keyName(s string) tea.KeyPressMsg {
	if s == "tab" {
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return key(s)
}

func TestAgentNameDropsRepeatedTicketKey(t *testing.T) {
	m, _ := newModel()
	m.Update(agentsMsg{agents: []agent.Agent{
		{Tool: "claude", Name: "ABC-1 fix-the-thing", Branch: "abc-1-x", Status: agent.Idle},
		{Tool: "claude", Name: "ABC-1", Branch: "abc-1-y", Status: agent.Idle},
	}})
	v := view(m)
	if !strings.Contains(v, "○ fix-the-thing  idle") || !strings.Contains(v, "○ agent  idle") || strings.Contains(v, "○ ABC-1") {
		t.Fatalf("view:\n%s", v)
	}
}
