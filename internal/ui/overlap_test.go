package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/tracker"
)

func TestSiblingsTellTheAgentWhatsTakenElsewhere(t *testing.T) {
	m, _ := controlModel(t)
	m.issues = []tracker.Issue{
		{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", Project: "Launch"},
		{Key: "ABC-2", Title: "set up DNS", Team: "Alpha", State: "In Progress", Project: "Launch"},
		{Key: "ABC-3", Title: "unrelated", Team: "Alpha", State: "Todo"},
	}
	m.agents[0].TicketKey = "ABC-2" // an agent is on ABC-2
	got, n := m.siblings("ABC-1")
	if n != 1 || !strings.Contains(got, `project "Launch"`) || !strings.Contains(got, "- ABC-2 set up DNS (In Progress, an agent is working on it)") ||
		strings.Contains(got, "ABC-3") || !strings.Contains(got, "Stay within ABC-1") {
		t.Fatalf("%d:\n%s", n, got)
	}
	if got, _ := m.siblings("ABC-3"); got != "" {
		t.Fatalf("no project, but: %s", got)
	}
}

func edit(m *Model, id, file string) tea.Cmd {
	_, cmd := m.Update(hookMsg(hook.Event{Agent: id, Tool: "claude", Event: "PreToolUse", Reply: func([]byte) {},
		Payload: []byte(`{"tool_name":"Edit","tool_input":{"file_path":"` + file + `"}}`)}))
	return cmd
}

func TestOverlapAcrossWorktrees(t *testing.T) {
	m, _, notes := hookModel(t)
	m.live[0].Repo, m.live[0].Worktree = "/src/app", "/src/app/.worktrees/abc-1"
	m.live[1].Repo, m.live[1].Worktree, m.live[1].TicketKey = "/src/app", "/src/app/.worktrees/abc-2", "ABC-2"
	run := func(c tea.Cmd) {
		if c == nil {
			return
		}
		if b, ok := c().(tea.BatchMsg); ok {
			for _, x := range b {
				if x != nil {
					if bb, ok := x().(tea.BatchMsg); ok {
						for _, y := range bb {
							if y != nil {
								y()
							}
						}
					}
				}
			}
		}
	}
	run(edit(m, "r1", "/src/app/.worktrees/abc-1/dns.tf"))
	run(edit(m, "r2", "/src/app/.worktrees/abc-2/main.tf")) // a different file: fine
	if len(*notes) != 0 {
		t.Fatalf("warned too early: %v", *notes)
	}
	run(edit(m, "r2", "/src/app/.worktrees/abc-2/dns.tf"))
	run(edit(m, "r2", "/src/app/.worktrees/abc-2/dns.tf")) // once per file, not per edit
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "is changing dns.tf, which ABC-1 already changed") {
		t.Fatalf("notes %v", *notes)
	}
	if !rowHas(view(m), "two", "⚑ same files as ABC-1") {
		t.Fatalf("row:\n%s", view(m))
	}
}
