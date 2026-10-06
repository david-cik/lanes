package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/gh"
	"github.com/david-cik/lanes/internal/gitinfo"
)

type fakeReaders struct {
	gitCalls, prCalls []string
	pr                *gh.PR
	prErr             error
	opened            []string
}

func (f *fakeReaders) readers() Readers {
	return Readers{
		Git: func(_ context.Context, dir string) (gitinfo.Info, error) {
			f.gitCalls = append(f.gitCalls, dir)
			return gitinfo.Info{Branch: "abc-1/x", Base: "origin/main", BaseAhead: 3, Dirty: 2, Subject: "fix: guard nil", When: time.Unix(0, 0)}, nil
		},
		PR: func(_ context.Context, dir, branch string) (*gh.PR, error) {
			f.prCalls = append(f.prCalls, dir+"@"+branch)
			return f.pr, f.prErr
		},
		Open: func(url string) error { f.opened = append(f.opened, url); return nil },
	}
}

func detailsModel(t *testing.T) (*Model, *fakeReaders) {
	m, _ := controlModel(t)
	fr := &fakeReaders{pr: &gh.PR{Number: 41, URL: "https://github.com/acme/app/pull/41", State: "OPEN", Review: "APPROVED",
		Checks: gh.Checks{Pass: 12, Fail: 1, Pending: 2}, Failed: []string{"lint"}}}
	m.readers = fr.readers()
	snap := m.opt.Fleet.(*fleetSnap)
	for i := range snap.agents {
		snap.agents[i].Cwd, snap.agents[i].Branch = "/w/"+snap.agents[i].Name, "abc-1/x"
	}
	m.agents = append([]agent.Agent{}, snap.agents...)
	m.rebuild()
	return m, fr
}

// settle runs a command tree, feeding every message it yields back into the model.
func settle(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			settle(m, c)
		}
	case nil:
	default:
		_, next := m.Update(msg)
		settle(m, next)
	}
}

func TestDetailsForAgent(t *testing.T) {
	m, fr := detailsModel(t)
	m.cursorTo(t, "one")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	v := view(m)
	for _, want := range []string{"── details", "abc-1/x", "↑3 ↓0 vs origin/main", "2 uncommitted", "last: fix: guard nil",
		"#41 open", "approved", "✓12", "✗1", "…2", "failing: lint", "recent"} {
		if !strings.Contains(v, want) {
			t.Errorf("details missing %q:\n%s", want, v)
		}
	}
	if len(fr.gitCalls) != 1 || len(fr.prCalls) != 1 {
		t.Fatalf("calls git=%v pr=%v", fr.gitCalls, fr.prCalls)
	}
}

func TestDetailsDebounceAndCache(t *testing.T) {
	m, fr := detailsModel(t)
	m.cursorTo(t, "one")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	// three quick moves: only the last tick may fetch
	var ticks []tea.Cmd
	for _, k := range []string{"j", "j", "k"} {
		_, c := m.Update(key(k))
		ticks = append(ticks, c)
	}
	for _, c := range ticks {
		settle(m, c)
	}
	if len(fr.prCalls) != 2 || fr.prCalls[1] != "/w/two@abc-1/x" {
		t.Fatalf("pr calls %v", fr.prCalls)
	}
	// back to a cached row within the TTL: no new calls
	m.cursorTo(t, "one")
	_, c := m.Update(key("j"))
	m.cursorTo(t, "one")
	settle(m, c)
	if len(fr.prCalls) != 2 {
		t.Fatalf("cache ignored: %v", fr.prCalls)
	}
	// r forces (it also refreshes tickets and agents, which need a tracker/fleet)
	settle(m, m.fetchDetails(true))
	if len(fr.prCalls) != 3 {
		t.Fatalf("r did not refresh: %v", fr.prCalls)
	}
}

func TestDetailsNoPRAndUnavailable(t *testing.T) {
	m, fr := detailsModel(t)
	fr.pr = nil
	m.cursorTo(t, "one")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	if v := view(m); !strings.Contains(v, "no PR for abc-1/x") {
		t.Fatalf("view:\n%s", v)
	}
	fr.prErr = gh.Unavailable{Reason: "gh is not installed"}
	settle(m, m.fetchDetails(true))
	if v := view(m); !strings.Contains(v, "gh is not installed") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestDetailsExternalAndTicket(t *testing.T) {
	m, _ := detailsModel(t)
	m.cursorTo(t, "outside")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	if v := view(m); !strings.Contains(v, "not started by lanes — no activity log") {
		t.Fatalf("external:\n%s", v)
	}
	m.cursorTo(t, "ABC-1 fix it")
	settle(m, m.fetchDetails(false))
	if v := view(m); !strings.Contains(v, "pull requests") || !strings.Contains(v, "on this ticket") {
		t.Fatalf("ticket:\n%s", v)
	}
}

func TestOpenKey(t *testing.T) {
	m, fr := detailsModel(t)
	m.cursorTo(t, "one")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	_, cmd = m.Update(key("o"))
	settle(m, cmd)
	if len(fr.opened) != 1 || fr.opened[0] != "https://github.com/acme/app/pull/41" {
		t.Fatalf("opened %v", fr.opened)
	}
	m.cursorTo(t, "outside") // nothing cached for it yet
	m.Update(key("o"))
	if len(fr.opened) != 1 || !strings.Contains(m.notice, "nothing to open") {
		t.Fatalf("opened %v notice %q", fr.opened, m.notice)
	}
}

func TestDetailsOnShortPanelTakeWholeBody(t *testing.T) {
	m, _ := detailsModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
	m.cursorTo(t, "one")
	_, cmd := m.Update(key("d"))
	settle(m, cmd)
	v := view(m)
	if strings.Contains(v, "ABC-1 fix it") || !strings.Contains(v, "── details") {
		t.Fatalf("short panel should show details only:\n%s", v)
	}
	_ = agent.Working
	_ = errors.New
}
