package fleet

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
)

type panes struct {
	ps  []tmux.Pane
	err error
}

func (p panes) Panes() ([]tmux.Pane, error) { return p.ps, p.err }

type lister []agent.Agent

func (l lister) Name() string                                { return "claude" }
func (l lister) List(context.Context) ([]agent.Agent, error) { return l, nil }
func (lister) Command(agent.LaunchSpec) agent.Command        { return agent.Command{} }
func (lister) Stop() (string, []string)                      { return "", nil }

func TestSnapshotMergesLaunchedAndExternal(t *testing.T) {
	store := state.Store{Dir: t.TempDir()}
	store.Save(state.Record{ID: "r1", Tool: "claude", TicketKey: "ABC-1", SessionID: "s1", Pane: "%1", Branch: "abc-1/x"})
	store.Save(state.Record{ID: "gone", Tool: "claude", TicketKey: "ABC-2", Pane: "%2"})
	f := Fleet{
		Adapters: []agent.Adapter{lister{
			{ID: "s1", Tool: "claude", Name: "ABC-1 x", Status: agent.Working},
			{ID: "s9", Tool: "claude", Name: "other", Status: agent.Idle},
		}},
		Store: store,
		Tmux:  panes{ps: []tmux.Pane{{ID: "%5", Agent: "r1"}, {ID: "%2"}}},
	}
	got, live, err := f.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != "r1" || live[0].Pane != "%5" {
		t.Fatalf("live %+v", live)
	}
	if len(got) != 2 {
		t.Fatalf("agents %+v", got)
	}
	m, e := got[0], got[1]
	if m.External || m.RecordID != "r1" || m.Pane != "%5" || m.Status != agent.Working || m.TicketKey != "ABC-1" {
		t.Fatalf("managed %+v", m)
	}
	if !e.External || e.ID != "s9" {
		t.Fatalf("external %+v", e)
	}
}

func TestSnapshotDoesNotPruneWhenTmuxFails(t *testing.T) {
	store := state.Store{Dir: t.TempDir()}
	store.Save(state.Record{ID: "r1", Tool: "claude", Pane: "%1"})
	f := Fleet{Store: store, Tmux: panes{err: errors.New("tmux broke")}}
	got, live, err := f.Snapshot(context.Background())
	if err == nil || len(live) != 1 || len(got) != 1 {
		t.Fatalf("got=%+v live=%+v err=%v", got, live, err)
	}
	if all, _ := store.All(); len(all) != 1 {
		t.Fatal("record deleted after tmux failure")
	}
}

type noPanes struct{ t *testing.T }

func (n noPanes) Panes() ([]tmux.Pane, error) {
	n.t.Fatal("NoPrune run listed tmux panes")
	return nil, nil
}

func TestNoPruneNeverTouchesTmux(t *testing.T) {
	store := state.Store{Dir: t.TempDir()}
	store.Save(state.Record{ID: "r1", Tool: "claude", Pane: "%1"})
	_, live, err := Fleet{Store: store, Tmux: noPanes{t}, NoPrune: true}.Snapshot(context.Background())
	if err != nil || len(live) != 1 {
		t.Fatalf("live=%+v err=%v", live, err)
	}
}

func TestUnlistedLaunchedAgentIsNamed(t *testing.T) {
	store := state.Store{Dir: t.TempDir()}
	store.Save(state.Record{ID: "r1", Tool: "claude", Name: "ABC-1 fix-it", SessionID: "gone", Pane: "%1", Worktree: "/w/git"})
	store.Save(state.Record{ID: "r2", Tool: "claude", TicketKey: "ABC-2", SessionID: "gone2", Pane: "%2", Worktree: "/w/git"}) // older record: no name
	got, _, err := Fleet{Store: store, Tmux: panes{ps: []tmux.Pane{{ID: "%1", Agent: "r1"}, {ID: "%2", Agent: "r2"}}}}.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, a := range got {
		names[a.RecordID] = a.Name
	}
	if names["r1"] != "ABC-1 fix-it" || names["r2"] != "ABC-2" {
		t.Fatalf("names %v", names)
	}
}

// screens is tmux with one pane whose content the test sets.
type screens struct {
	panes
	screen *string
}

func (s screens) Capture(string) (string, error) { return *s.screen, nil }

type waiter struct{ lister }

func (waiter) Name() string          { return "x" }
func (waiter) WaitingText() []string { return []string{"Allow it?"} }

func TestPaneStatusForToolsWithoutHooks(t *testing.T) {
	store := state.Store{Dir: t.TempDir()}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Save(state.Record{ID: "r1", Tool: "x", Pane: "%1", CreatedAt: clock})
	store.Save(state.Record{ID: "old", Tool: "x", Pane: "%2", CreatedAt: clock.Add(-time.Hour)})
	screen := "thinking…"
	f := Fleet{Adapters: []agent.Adapter{waiter{}}, Store: store, Seen: &PaneMemory{},
		Tmux: screens{panes{ps: []tmux.Pane{{ID: "%1", Agent: "r1"}, {ID: "%2", Agent: "old"}}}, &screen},
		Now:  func() time.Time { return clock }}
	first := true
	status := func() agent.Status {
		got, _, err := f.Snapshot(context.Background())
		if err != nil || len(got) != 2 {
			t.Fatalf("%v %v", got, err)
		}
		if i := slices.IndexFunc(got, func(a agent.Agent) bool { return a.RecordID == "old" }); first && got[i].Status != agent.Idle {
			t.Fatalf("an agent lanes finds after a restart reads as working: %+v", got[i])
		}
		first = false
		return got[slices.IndexFunc(got, func(a agent.Agent) bool { return a.RecordID == "r1" })].Status
	}
	for i, step := range []struct {
		after  time.Duration // since the last look
		screen string
		want   agent.Status
	}{
		{0, "thinking…", agent.Working},           // first look
		{time.Second, "thinking…", agent.Working}, // the same, but not for long
		{2 * time.Second, "thinking… done", agent.Working},
		{7 * time.Second, "thinking… done", agent.Idle}, // quiet a while
		{time.Second, "Allow it? (y/n)", agent.Waiting},
		{10 * time.Second, "Allow it? (y/n)", agent.Waiting},
		{time.Second, "Allow it? (y/n) y\n1\n2\n3\n4\n5\n6\n7\n8", agent.Working}, // answered: scrolled up
		{7 * time.Second, "Allow it? (y/n) y\n1\n2\n3\n4\n5\n6\n7\n8", agent.Idle},
	} {
		clock = clock.Add(step.after)
		screen = step.screen
		if got := status(); got != step.want {
			t.Fatalf("step %d: %s, want %s", i, got, step.want)
		}
	}
}
