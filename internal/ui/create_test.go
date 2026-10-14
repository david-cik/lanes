package ui

import (
	"context"
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

func TestNewTicket(t *testing.T) {
	m, _ := controlModel(t)
	m.issues[0].Project = "Launch"
	m.rebuild()
	ct := &creatingTracker{fakeTracker: &fakeTracker{}, list: m.issues}
	m.opt.Tracker = ct
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("N"))
	if m.modal == nil || !strings.Contains(m.modal.title, "New Alpha ticket") || !strings.Contains(strings.Join(m.modal.lines, " "), "project Launch") {
		t.Fatalf("title prompt %+v", m.modal)
	}
	for _, r := range "Rotate keys" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	for _, r := range "all of them" {
		m.Update(key(string(r)))
	}
	_, cmd := m.Update(key("enter"))
	_, cmd = m.Update(cmd()) // created → refresh
	m.Update(cmd())          // the refreshed list has it
	if len(ct.made) != 1 || ct.made[0] != "Alpha|Rotate keys|all of them|Launch" {
		t.Fatalf("made %v", ct.made)
	}
	if r, _ := m.selected(); r.Issue == nil || r.Issue.Key != "ABC-9" || !strings.Contains(m.notice, "created ABC-9") {
		t.Fatalf("cursor on %+v, notice %q", r, m.notice)
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
