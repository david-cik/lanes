package board

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/tracker"
)

func render(rows []Row) string {
	var sb strings.Builder
	for _, r := range rows {
		txt := r.Text
		if r.Agent != nil {
			txt = "@" + r.Agent.Name
		}
		fmt.Fprintf(&sb, "%s%s\n", strings.Repeat(" ", r.Level), txt)
	}
	return sb.String()
}

func TestBuild(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issues := []tracker.Issue{
		{Key: "XYZ-1", Title: "x one", Team: "Xray", State: "Todo", StateType: "unstarted", UpdatedAt: t0},
		{Key: "ABC-2", Title: "older", Team: "Alpha", State: "In Progress", StateType: "started", UpdatedAt: t0},
		{Key: "ABC-3", Title: "newer", Team: "Alpha", State: "In Progress", StateType: "started", UpdatedAt: t0.Add(time.Hour)},
		{Key: "ABC-4", Title: "todo", Team: "Alpha", State: "Todo", StateType: "unstarted", UpdatedAt: t0},
	}
	agents := []agent.Agent{
		{Name: "impl", Branch: "abc-2-older"},
		{Name: "review", Branch: "abc-2-older"},
		{Name: "closed-ticket", Branch: "abc-99-gone"},
		{Name: "scratch", Branch: "main"},
	}
	want := `Alpha
 Todo
  ABC-4 todo
 In Progress
  ABC-3 newer
  ABC-2 older
   @impl
   @review
Xray
 Todo
  XYZ-1 x one
Unlinked
 @closed-ticket
 @scratch
`
	got := render(Build(issues, agents))
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if agents[0].TicketKey != "ABC-2" || agents[2].TicketKey != "ABC-99" {
		t.Fatalf("ticket keys not recorded: %+v", agents)
	}
}

func TestBuildEmpty(t *testing.T) {
	if rows := Build(nil, nil); len(rows) != 0 {
		t.Fatalf("got %+v", rows)
	}
}
