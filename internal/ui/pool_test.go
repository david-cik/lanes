package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/tracker"
)

// poolTracker also offers unassigned tickets and records claims.
type poolTracker struct {
	detailTracker
	claimed []string
}

func (p *poolTracker) Issues(context.Context, string) ([]tracker.Issue, error) {
	return []tracker.Issue{{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted"}}, nil
}
func (p *poolTracker) Pool(_ context.Context, teams []string) ([]tracker.Issue, error) {
	if len(teams) != 1 || teams[0] != "Alpha" {
		return nil, nil
	}
	return []tracker.Issue{{Key: "ABC-7", Title: "grab me", Team: "Alpha", State: "Todo", StateType: "unstarted", Pool: true}}, nil
}
func (p *poolTracker) Issue(_ context.Context, key string) (tracker.IssueDetail, error) {
	return tracker.IssueDetail{Issue: tracker.Issue{Key: key, Title: "grab me", Team: "Alpha"}}, nil
}
func (p *poolTracker) Claim(_ context.Context, key, team string, order []string) (string, error) {
	p.claimed = append(p.claimed, key+"@"+team)
	return "In Progress", nil
}

// run executes a command (one level of a batch) and feeds its messages to the model.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			if c != nil {
				m.Update(c())
			}
		}
		return
	}
}

func TestPoolLaneOpensAndClaimsOnStart(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	pt := &poolTracker{detailTracker: detailTracker{&fakeTracker{}}}
	m.opt.Tracker = pt
	m.Update(m.fetchIssues()())
	if v := view(m); !rowHas(v, "▸ UP FOR GRABS", "1") || strings.Contains(v, "grab me") {
		t.Fatalf("closed pool lane:\n%s", v)
	}
	m.cursorTo(t, "UP FOR GRABS")
	m.Update(key("enter"))
	if v := view(m); !strings.Contains(v, "▾ UP FOR GRABS") || !strings.Contains(v, "ABC-7 grab me") {
		t.Fatalf("open pool lane:\n%s", v)
	}
	m.cursorTo(t, "grab me")
	_, cmd := m.Update(key("n"))
	_, cmd = m.Update(cmd())
	m.Update(cmd())
	if l := strings.Join(m.modal.lines, "\n"); !strings.Contains(l, "assign to you") {
		t.Fatalf("confirm:\n%s", l)
	}
	m.Update(key("c"))
	if l := strings.Join(m.modal.lines, "\n"); !strings.Contains(l, "leave it unassigned") {
		t.Fatalf("after c:\n%s", l)
	}
	m.Update(key("c"))
	_, cmd = m.Update(key("y"))
	run(m, cmd)
	if len(pt.claimed) != 1 || pt.claimed[0] != "ABC-7@Alpha" || !strings.Contains(m.notice, "claimed ABC-7") {
		t.Fatalf("claimed %v notice %q", pt.claimed, m.notice)
	}
}

func TestPoolTicketNotClaimedWhenLeft(t *testing.T) {
	m, _ := controlModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	pt := &poolTracker{detailTracker: detailTracker{&fakeTracker{}}}
	m.opt.Tracker = pt
	m.Update(m.fetchIssues()())
	m.poolOpen = true
	m.rebuild()
	m.cursorTo(t, "grab me")
	_, cmd := m.Update(key("n"))
	_, cmd = m.Update(cmd())
	m.Update(cmd())
	m.Update(key("c"))
	_, cmd = m.Update(key("y"))
	run(m, cmd)
	if len(pt.claimed) != 0 {
		t.Fatalf("claimed %v", pt.claimed)
	}
}
