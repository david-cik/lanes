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
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/launch"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

// A project's coordinator is a Claude session on no ticket that looks after the
// project's tickets and agents through the lanes MCP tools (`lanes mcp`, which makes
// the control requests in control.go). There is at most one per project.

// coordinatorPrompt is the coordinator's first prompt, told what it may do under cc.
func coordinatorPrompt(project string, cc config.CoordinatorConfig) string {
	start := "start starts an agent on a ticket right away and returns its id (the user may be asked to allow the call first)."
	if cc.Start == "panel" {
		start = "start only asks the user: they confirm in the lanes panel, and wait reports \"started\" if they say yes."
	}
	answer := "When an agent needs the user (a permission request, or the folder-trust prompt of a new folder), tell the user what it asks; if they want it allowed or denied, use approve or deny, repeating the request exactly as agents or wait showed it."
	switch cc.Approve {
	case "auto":
		answer = "When an agent needs the user (a permission request, or the folder-trust prompt of a new folder), you may answer it with approve or deny, repeating the request exactly as agents or wait showed it. Allow only what its ticket needs; deny anything destructive or outside it, and tell the user what you answered."
	case "off":
		answer = "When an agent needs the user (a permission prompt), tell the user: you can't answer it, they do that in the lanes panel."
	}
	return strings.NewReplacer("{project}", project, "{start}", start, "{answer}", answer).Replace(`You coordinate the work on the project "{project}" in lanes, a board of tickets and the coding agents working them. You don't change code yourself: you keep the project's agents moving, stop them from duplicating each other, and keep the user informed. The user may run the project entirely through you.

Use the lanes tools (agents, start, send, read, wait, approve, deny, stop). Call agents with project "{project}" to see its tickets and the agents on each. {start} Name an agent by its id from agents, or by its ticket. Loop on wait while agents work, passing as after the seq from your last agents, start, or wait answer; an agent's waiting field in agents shows a prompt that was already open before you began waiting. read an agent when it's ready. {answer} When starting agents on related tickets, tell each one which parts the others own. stop ends an agent (its worktree is kept); only stop one the user wants stopped.

Start by calling agents for "{project}", summarise where the project stands in a few lines, and ask the user what they want done.`)
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
	// Where it runs barely matters (it changes nothing), but Claude asks before working
	// in a folder it hasn't been told to trust: the launch folder or a repo root, which
	// agents already run in, are likelier to be trusted than home.
	dir, _ := os.UserHomeDir()
	for _, d := range append([]string{m.opt.Config.LaunchDir}, m.opt.Config.RepoRoots...) {
		if fi, err := os.Stat(d); d != "" && err == nil && fi.IsDir() {
			dir = d
			break
		}
	}
	bin := cmp.Or(m.opt.HookBin, "lanes")
	// What it may do without Claude asking you first; stop always asks.
	cc := m.opt.Config.Coordinator
	tools := []string{"agents", "send", "read", "wait"}
	if cc.Start == "auto" || cc.Start == "panel" { // "panel": the panel asks
		tools = append(tools, "start")
	}
	if cc.Approve != "off" {
		tools = append(tools, "deny")
	}
	if cc.Approve == "auto" {
		tools = append(tools, "approve")
	}
	var rules []string
	for _, c := range tools {
		rules = append(rules, "mcp__lanes__"+c)
	}
	// "manual" is Claude's ask-every-time mode: in auto or bypass mode (a default in your
	// settings), Claude might run a tool left off the list above without asking you.
	mode := []string{"--permission-mode=manual"}
	p := launch.Plan{
		Ticket: tracker.IssueDetail{Issue: tracker.Issue{Key: state.NoTicket}}, Repo: launch.Repo{Name: filepath.Base(dir), Path: dir},
		Adapter: a, Worktree: dir, InPlace: true, Name: "coordinator · " + project, Project: project,
		// "=" keeps these variadic flags from taking the prompt after them as a value.
		Args: append(slices.Clone(m.opt.Config.Agents[a.Name()].Args),
			append(mode, "--mcp-config="+mcpConfig(bin), "--allowedTools="+strings.Join(rules, ","))...),
		Prompt:  coordinatorPrompt(project, cc),
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
		"starting agents:   " + map[string]string{"ask": "Claude asks you in its pane first", "auto": "on its own",
			"panel": "you confirm each one here"}[cc.Start],
		"agents' prompts:   " + map[string]string{"ask": "it can deny; each allow asks you in its pane",
			"auto": "it allows or denies them itself", "off": "yours, in this panel"}[cc.Approve],
		"stopping agents:   Claude asks you in its pane first",
		faint.Render("(coordinator.start / coordinator.approve in the config)"),
	}, actions: []action{{"y", "start", start}, {"enter", "start", start}}}
	return nil
}
