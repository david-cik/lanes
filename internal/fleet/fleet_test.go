package fleet

import (
	"context"
	"errors"
	"testing"

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
