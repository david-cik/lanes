// Package fleet assembles the agents lanes shows: the ones it launched (from saved
// records and live tmux panes) plus every other session the adapters can see.
package fleet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
)

type Panes interface {
	Panes() ([]tmux.Pane, error)
}

// Capturer is the part of tmux that shows what a pane displays.
type Capturer interface {
	Capture(pane string) (string, error)
}

type Fleet struct {
	Adapters []agent.Adapter
	Store    state.Store
	Tmux     Panes
	// NoPrune keeps records even when their panes aren't found, for read-only runs
	// that may not be looking at the tmux server the agents live on.
	NoPrune bool
	// Seen remembers what the panes of agents without hooks last showed (their status
	// comes from that); nil = such agents' status is unknown.
	Seen *PaneMemory
	Now  func() time.Time // nil = time.Now
}

func (f Fleet) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// PaneMemory is what each hookless agent's pane last looked like.
type PaneMemory struct {
	mu   sync.Mutex
	last map[string]paneSeen // by launch record
}

type paneSeen struct {
	sum     [32]byte
	changed time.Time // when it last looked different
	status  agent.Status
	since   time.Time
}

// quiet is how long a pane must stay the same to count as idle: a tool between two
// lines of output isn't done.
const quiet = 6 * time.Second

// paneStatus reads a hookless agent's status from its pane: text its tool shows when it
// needs you = waiting; changed in the last few seconds = working; quiet = idle.
// ponytail: a tool whose idle screen animates (a clock, a spinner) reads as working;
// such a tool needs hooks, or a pattern for its idle screen.
func (f Fleet) paneStatus(r state.Record, ad agent.Adapter, now time.Time) (agent.Status, time.Time) {
	c, ok := f.Tmux.(Capturer)
	if !ok || f.Seen == nil || r.Pane == "" {
		return agent.Unknown, r.CreatedAt
	}
	screen, err := c.Capture(r.Pane)
	if err != nil {
		return agent.Unknown, r.CreatedAt
	}
	sum := sha256.Sum256([]byte(screen))
	f.Seen.mu.Lock()
	defer f.Seen.mu.Unlock()
	if f.Seen.last == nil {
		f.Seen.last = map[string]paneSeen{}
	}
	prev, seen := f.Seen.last[r.ID]
	changed := prev.changed
	if !seen || sum != prev.sum {
		changed = now
	}
	status := agent.Idle
	switch {
	case waiting(screen, ad):
		status = agent.Waiting
	case now.Sub(changed) < quiet:
		status = agent.Working
	}
	since := prev.since
	if !seen || status != prev.status {
		since = now
	}
	f.Seen.last[r.ID] = paneSeen{sum, changed, status, since}
	return status, since
}

// waiting says whether the bottom of screen, where a question still waiting for an
// answer sits, shows one of the tool's waiting texts. An answered one higher up, in what
// the tool printed since, doesn't count.
func waiting(screen string, ad agent.Adapter) bool {
	w, ok := ad.(agent.Waiter)
	if !ok {
		return false
	}
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	bottom := strings.Join(lines[max(0, len(lines)-waitLines):], "\n")
	for _, t := range w.WaitingText() {
		if t != "" && strings.Contains(bottom, t) {
			return true
		}
	}
	return false
}

// waitLines is how far up from the bottom a question can be: a dialog plus a footer.
const waitLines = 8

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
			Since: r.CreatedAt, TicketKey: r.TicketKey, RecordID: r.ID, Pane: r.Pane, Project: r.Project,
		}
		if ad := adapterNamed(f.Adapters, r.Tool); ad != nil {
			if _, hooks := ad.(agent.Hooker); !hooks && !f.NoPrune {
				a.Status, a.Since = f.paneStatus(r, ad, f.now())
			}
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

func adapterNamed(ads []agent.Adapter, name string) agent.Adapter {
	for _, a := range ads {
		if a.Name() == name {
			return a
		}
	}
	return nil
}
