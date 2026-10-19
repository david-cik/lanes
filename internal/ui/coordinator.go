package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/board"
	"github.com/david-cik/lanes/internal/launch"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

// A project's coordinator is a Claude session on no ticket that looks after the
// project's tickets and agents through `lanes agents|start|send|read|wait` (see
// control.go). There is at most one per project.

// coordinatorPrompt is the coordinator's first prompt; bin is how it runs lanes.
func coordinatorPrompt(project, bin string) string {
	return strings.ReplaceAll(strings.ReplaceAll(`You coordinate the work on the project "{project}" in lanes, a board of tickets and the coding agents working them. You don't change code yourself: you keep the project's agents moving, stop them from duplicating each other, and keep the user informed.

Commands (run them with Bash; they print JSON):
- {bin} agents --project "{project}": the project's tickets, their states, and the agents on each (status, ready, waiting, recent activity)
- {bin} start <TICKET> [instructions…]: ask the user to start an agent on a ticket. They confirm in the lanes panel; the instructions are added to the agent's first prompt.
- {bin} send <agent> <text…>: tell an agent something, as if the user typed it
- {bin} read <agent>: what an agent wrote since its last prompt
- {bin} wait --after <seq> [agent or TICKET…]: wait up to 90 seconds for an agent to start, finish a turn ("ready"), need the user, or end. Pass back the seq from the previous answer so nothing is missed.

Name an agent by its id from {bin} agents, or by its ticket. When an agent needs the user (a permission prompt), tell the user: you can't answer it. When starting agents on related tickets, tell each one which parts the others own.

Start by running {bin} agents --project "{project}", summarise where the project stands in a few lines, and ask the user what they want done.`,
		"{project}", project), "{bin}", bin)
}

// lanesBin is how a coordinator should run lanes: "lanes" if that is this executable
// on the PATH, else its full path.
func lanesBin(self string) string {
	if p, err := exec.LookPath("lanes"); err == nil && self != "" {
		a, errA := filepath.EvalSymlinks(p)
		b, errB := filepath.EvalSymlinks(self)
		if errA == nil && errB == nil && a == b {
			return "lanes"
		}
	}
	if self == "" {
		return "lanes"
	}
	return self
}

// selectedProject is the project of the row under the cursor: a project heading, a
// ticket in a project, an agent on one, or a coordinator.
func (m *Model) selectedProject() string {
	r, ok := m.selected()
	if !ok {
		return ""
	}
	switch {
	case r.Kind == board.ProjectRow:
		return r.Text
	case r.Kind == board.AgentRow && r.Agent.Project != "":
		return r.Agent.Project
	}
	if i, ok := m.selectedIssue(); ok {
		return i.Project
	}
	return ""
}

// startCoordinator shows the selected project's coordinator, or offers to start one.
func (m *Model) startCoordinator() tea.Cmd {
	if m.opt.Tmux == nil {
		m.say("", fmt.Errorf("run lanes inside tmux to launch agents"))
		return nil
	}
	project := m.selectedProject()
	if project == "" {
		m.say("", fmt.Errorf("select a project, or a ticket in one"))
		return nil
	}
	if i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.Project == project }); i >= 0 {
		m.focus(m.live[i].ID, m.live[i].Pane)
		m.say(project+" already has its coordinator", nil)
		return nil
	}
	a := m.adapter("claude")
	if _, ok := a.(agent.Hooker); !ok || a.Name() != "claude" {
		m.say("", fmt.Errorf("a coordinator runs in Claude Code, which isn't set up as an agent"))
		return nil
	}
	dir := m.opt.Config.LaunchDir
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	bin := lanesBin(m.opt.HookBin)
	var rules []string
	for _, c := range []string{"agents", "start", "send", "read", "wait"} {
		rules = append(rules, "Bash("+bin+" "+c+":*)")
	}
	p := launch.Plan{
		Ticket: tracker.IssueDetail{Issue: tracker.Issue{Key: state.NoTicket}}, Repo: launch.Repo{Name: filepath.Base(dir), Path: dir},
		Adapter: a, Worktree: dir, InPlace: true, Name: "coordinator · " + project, Project: project,
		// "=" keeps the variadic flag from taking the prompt after it as another rule.
		Args:    append(slices.Clone(m.opt.Config.Agents[a.Name()].Args), "--allowedTools="+strings.Join(rules, ",")),
		Prompt:  coordinatorPrompt(project, bin),
		HookBin: m.opt.HookBin, Socket: m.opt.Socket,
	}
	tm, store := m.opt.Tmux, m.opt.Store
	start := func() (tea.Cmd, bool) {
		m.say("starting the coordinator of "+project+"…", nil)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			rec, err := launch.Run(ctx, p, tm, store)
			return launchedMsg{rec: rec, err: err}
		}, false
	}
	m.notice = ""
	m.modal = &modal{title: "Start a coordinator for " + project + "?", lines: []string{
		"tool:     claude",
		"folder:   " + home(dir),
		"",
		"It watches the project's tickets and agents, and talks to the agents for you.",
		"It runs lanes agents / send / read / wait without asking;",
		"lanes start asks you here before any agent starts.",
		"It can't answer an agent's permission prompts: those stay yours.",
	}, actions: []action{{"y", "start", start}, {"enter", "start", start}}}
	return nil
}
