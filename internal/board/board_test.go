package board

import (
	"fmt"

	"github.com/david-cik/lanes/internal/state"
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
	got := render(Build(issues, agents, nil, nil))
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if agents[0].TicketKey != "ABC-2" || agents[2].TicketKey != "ABC-99" {
		t.Fatalf("ticket keys not recorded: %+v", agents)
	}
}

func TestBuildEmpty(t *testing.T) {
	if rows := Build(nil, nil, nil, nil); len(rows) != 0 {
		t.Fatalf("got %+v", rows)
	}
}

func TestLinkPrecedenceAndStateOrder(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "ABC-1", Title: "one", Team: "Alpha", State: "In Progress", StateType: "started"},
		{Key: "ABC-2", Title: "two", Team: "Alpha", State: "In Review", StateType: "started"},
		{Key: "ABC-3", Title: "three", Team: "Alpha", State: "In Staging", StateType: "started"},
	}
	agents := []agent.Agent{
		{Tool: "claude", ID: "s1", Name: "preset", Branch: "abc-3-x", TicketKey: "ABC-1"}, // launch record wins over branch
		{Tool: "claude", ID: "s2", Name: "linked", Branch: "abc-3-x"},                     // manual link wins over branch
		{Tool: "claude", ID: "s3", Name: "inferred", Branch: "abc-3-x"},
	}
	links := map[string]string{"claude:s2": "ABC-2"}
	order := map[string][]string{"Alpha": {"In Progress", "In Staging"}}
	want := `Alpha
 In Progress
  ABC-1 one
   @preset
 In Staging
  ABC-3 three
   @inferred
 In Review
  ABC-2 two
   @linked
`
	if got := render(Build(issues, agents, links, order)); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRemovedFromTicketStaysUnlinked(t *testing.T) {
	issues := []tracker.Issue{{Key: "ABC-1", Title: "one", Team: "Alpha", State: "Todo", StateType: "unstarted"}}
	agents := []agent.Agent{
		{Tool: "claude", ID: "s1", Name: "removed", Branch: "abc-1-x"},          // branch matches, link says no
		{Tool: "claude", ID: "s2", Name: "launched", TicketKey: state.NoTicket}, // launch record says no
	}
	got := render(Build(issues, agents, map[string]string{"claude:s1": state.NoTicket}, nil))
	want := "Alpha\n Todo\n  ABC-1 one\nUnlinked\n @removed\n @launched\n"
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	if agents[0].TicketKey != "" {
		t.Fatal("sentinel leaked into TicketKey")
	}
}
