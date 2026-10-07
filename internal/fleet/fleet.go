// Package fleet assembles the agents lanes shows: the ones it launched (from saved
// records and live tmux panes) plus every other session the adapters can see.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
)

type Panes interface {
	Panes() ([]tmux.Pane, error)
}

type Fleet struct {
	Adapters []agent.Adapter
	Store    state.Store
	Tmux     Panes
	// NoPrune keeps records even when their panes aren't found, for read-only runs
	// that may not be looking at the tmux server the agents live on.
	NoPrune bool
}

// Snapshot returns all agents and the live launch records.
func (f Fleet) Snapshot(ctx context.Context) ([]agent.Agent, []state.Record, error) {
	var errs []error
	recs, err := f.Store.All()
	if err != nil {
		errs = append(errs, err)
	}
	if f.NoPrune {
		// read-only run: keep records as saved, don't depend on tmux at all
	} else if panes, err := f.Tmux.Panes(); err != nil {
		errs = append(errs, err) // keep records as they are; never prune blind
	} else {
		paneOf := map[string]string{}
		for _, p := range panes {
			if p.Agent != "" && !p.Dead {
				paneOf[p.Agent] = p.ID
			}
		}
		if recs, err = f.Store.Prune(recs, paneOf); err != nil {
			errs = append(errs, err)
		}
	}

	listed := map[string]agent.Agent{} // tool:sessionID → listed session
	var order []string
	for _, a := range f.Adapters {
		got, err := a.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
		}
		for _, g := range got {
			k := state.LinkKey(g.Tool, g.ID)
			listed[k] = g
			order = append(order, k)
		}
	}

	var out []agent.Agent
	for _, r := range recs {
		a := agent.Agent{
			ID: r.SessionID, Tool: r.Tool, Name: recordName(r), Cwd: r.Worktree, Status: agent.Unknown,
			Since: r.CreatedAt, TicketKey: r.TicketKey, RecordID: r.ID, Pane: r.Pane,
		}
		if r.SessionID != "" {
			k := state.LinkKey(r.Tool, r.SessionID)
			if l, ok := listed[k]; ok {
				a.Status, a.Name = l.Status, l.Name
				delete(listed, k)
			}
		}
		out = append(out, a)
	}
	for _, k := range order {
		if l, ok := listed[k]; ok {
			l.External = true
			out = append(out, l)
		}
	}
	agent.FillBranches(ctx, out)
	return out, recs, errors.Join(errs...)
}

// recordName names a launched agent the tool doesn't list (yet): the name given at
// launch, else its branch, else its ticket, else its folder.
func recordName(r state.Record) string {
	ticket := r.TicketKey
	if ticket == state.NoTicket {
		ticket = ""
	}
	for _, n := range []string{r.Name, r.Branch, ticket, filepath.Base(r.Worktree)} {
		if n != "" && n != "." && n != "/" {
			return n
		}
	}
	return r.ID
}

// ReconcileTmux is the part of tmux.Client crash recovery needs.
type ReconcileTmux interface {
	Panes() ([]tmux.Pane, error)
	KillPane(pane string) error
	HasSession(name string) bool
	NewSession(name, cwd string, env, argv []string) (string, error)
	Swap(a, b string) error
}

// Reconcile undoes what a crashed panel left behind: placeholder panes are killed and
// any agent pane stranded outside its lanes-<id> session is moved back home.
// Call it only when no other panel is running.
func Reconcile(tm ReconcileTmux) error {
	panes, err := tm.Panes()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range panes {
		if p.Placeholder {
			errs = append(errs, tm.KillPane(p.ID))
		}
	}
	for _, p := range panes {
		home := "lanes-" + p.Agent
		if p.Agent == "" || p.Session == home || tm.HasSession(home) {
			continue
		}
		dummy, err := tm.NewSession(home, "/", nil, []string{"sh", "-c", "exec sleep 600"})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, tm.Swap(p.ID, dummy), tm.KillPane(dummy))
	}
	return errors.Join(errs...)
}
