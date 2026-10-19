package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
// project's tickets and agents through the lanes MCP tools (`lanes mcp`, which makes
// the control requests in control.go). There is at most one per project.

// coordinatorPrompt is the coordinator's first prompt.
func coordinatorPrompt(project string) string {
	return strings.ReplaceAll(`You coordinate the work on the project "{project}" in lanes, a board of tickets and the coding agents working them. You don't change code yourself: you keep the project's agents moving, stop them from duplicating each other, and keep the user informed.

Use the lanes tools (agents, start, send, read, wait). Call agents with project "{project}" to see its tickets and the agents on each. start only asks the user: they confirm in the lanes panel. Name an agent by its id from agents, or by its ticket. Loop on wait (passing back its seq as after) while agents work. When an agent needs the user (a permission prompt), tell the user: you can't answer it. When starting agents on related tickets, tell each one which parts the others own.

Start by calling agents for "{project}", summarise where the project stands in a few lines, and ask the user what they want done.`,
		"{project}", project)
}

// mcpConfig is the --mcp-config that gives an agent the lanes tools (`bin mcp`).
func mcpConfig(bin string) string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"lanes": map[string]any{"type": "stdio", "command": bin, "args": []string{"mcp"}}}})
	return string(b)
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
	bin := cmp.Or(m.opt.HookBin, "lanes")
	var rules []string
	for _, c := range []string{"agents", "start", "send", "read", "wait"} {
		rules = append(rules, "mcp__lanes__"+c)
	}
	p := launch.Plan{
		Ticket: tracker.IssueDetail{Issue: tracker.Issue{Key: state.NoTicket}}, Repo: launch.Repo{Name: filepath.Base(dir), Path: dir},
		Adapter: a, Worktree: dir, InPlace: true, Name: "coordinator · " + project, Project: project,
		// "=" keeps these variadic flags from taking the prompt after them as a value.
		Args: append(slices.Clone(m.opt.Config.Agents[a.Name()].Args),
			"--mcp-config="+mcpConfig(bin), "--allowedTools="+strings.Join(rules, ",")),
		Prompt:  coordinatorPrompt(project),
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
		"It uses the lanes tools (agents, send, read, wait) without asking;",
		"its start asks you here before any agent starts.",
		"It can't answer an agent's permission prompts: those stay yours.",
	}, actions: []action{{"y", "start", start}, {"enter", "start", start}}}
	return nil
}
