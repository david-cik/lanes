// Package claude adapts Claude Code sessions.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/david-cik/lanes/internal/agent"
)

type Adapter struct {
	// run executes `claude agents --json`; tests replace it.
	run func(ctx context.Context) ([]byte, error)
}

func New() *Adapter {
	return &Adapter{run: func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	}}
}

func (*Adapter) Name() string { return "claude" }

// List lists running Claude sessions. A missing claude binary is not an error.
func (a *Adapter) List(ctx context.Context) ([]agent.Agent, error) {
	b, err := a.run(ctx)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude agents: %w", err)
	}
	var recs []struct {
		SessionID string `json:"sessionId"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Cwd       string `json:"cwd"`
		Status    string `json:"status"`
		StartedAt int64  `json:"startedAt"`
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("claude agents: %w", err)
	}
	out := make([]agent.Agent, 0, len(recs))
	for _, r := range recs {
		if r.Kind != "" && r.Kind != "interactive" && r.Kind != "background" {
			continue // run by a tool or plugin (Agent SDK, claude -p), not a person's session
		}
		out = append(out, agent.Agent{
			ID: r.SessionID, Tool: "claude", Name: r.Name, Cwd: r.Cwd,
			Status: status(r.Status), Since: time.UnixMilli(r.StartedAt), External: true,
		})
	}
	return out, nil
}

// inherited are variables that tie a process to a running Claude session. If lanes
// itself was started from inside Claude, a launched agent would inherit them and be
// treated as that session's child (taking its name, among other things).
var inherited = []string{
	"CLAUDECODE", "CLAUDE_PID", "CLAUDE_JOB_DIR", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_CODE_BRIDGE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT",
}

// CleanEnv is env without the variables that tie a process to a running Claude session.
func CleanEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(inherited, k) {
			out = append(out, kv)
		}
	}
	return out
}

// Command starts an interactive session with a pre-assigned id so lanes can find it
// again in `claude agents --json`.
func (*Adapter) Command(spec agent.LaunchSpec) agent.Command {
	id := uuid()
	argv := []string{"env"}
	for _, v := range inherited {
		argv = append(argv, "-u", v)
	}
	argv = append(argv, "claude")
	if spec.ResumeFrom != "" {
		argv = append(argv, "--resume", spec.ResumeFrom, "--fork-session") // verified: original keeps running
	}
	argv = append(argv, "--session-id", id)
	if spec.HookBin != "" {
		argv = append(argv, "--settings", HookSettings(spec.HookBin))
	}
	if spec.Name != "" {
		argv = append(argv, "-n", spec.Name)
	}
	argv = append(argv, spec.Args...)
	if spec.Prompt != "" {
		p := spec.Prompt
		if strings.HasPrefix(p, "-") { // don't let a prompt be parsed as a flag
			p = " " + p
		}
		argv = append(argv, p)
	}
	return agent.Command{Argv: argv, SessionID: id}
}

func (*Adapter) Stop() (string, []string) { return "/exit", nil }

func (*Adapter) CanFork() bool { return true }

// Ask runs a one-shot Claude with no tools, no saved session, and none of the user's
// settings (so no hooks fire), and returns its text answer.
func (a *Adapter) Ask(ctx context.Context, system, prompt string) (string, error) {
	argv := []string{"-p", "--tools", "", "--no-session-persistence", "--setting-sources", "",
		"--output-format", "json", "--system-prompt", system, "--model", "sonnet", prompt}
	cmd := exec.CommandContext(ctx, "claude", argv...)
	cmd.Env = CleanEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	var r struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if json.Unmarshal(out, &r) != nil {
		return string(out), nil
	}
	if r.IsError {
		line, _, _ := strings.Cut(strings.TrimSpace(r.Result), "\n")
		return "", fmt.Errorf("claude: %s", line)
	}
	return r.Result, nil
}

// CoordinatorArgs gives the session srv (as --mcp-config) with the allowed tools
// pre-allowed, in manual mode: an auto or bypass default in the user's settings could
// otherwise run a tool left off the list without asking. "=" keeps these variadic flags
// from taking the prompt after them as a value.
func (*Adapter) CoordinatorArgs(srv agent.MCPServer, allowed []string) []string {
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{srv.Name: map[string]any{
		"type": "stdio", "command": srv.Command, "args": srv.Args}}})
	var rules []string
	for _, t := range allowed {
		rules = append(rules, "mcp__"+srv.Name+"__"+t)
	}
	return []string{"--permission-mode=manual", "--mcp-config=" + string(cfg), "--allowedTools=" + strings.Join(rules, ",")}
}

func (*Adapter) TrustPrompt(screen string) bool {
	s := strings.ToLower(screen)
	return strings.Contains(s, "trust this folder") || strings.Contains(s, "do you trust the files")
}

// TrustKeys: the dialog lists "❯ No, exit" first and "Yes, I trust this folder" second.
func (*Adapter) TrustKeys(screen string) ([]string, bool) {
	if !strings.Contains(screen, "Yes, I trust this folder") {
		return nil, false
	}
	return []string{"Down", "Enter"}, true
}

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func status(s string) agent.Status {
	switch s {
	case "busy":
		return agent.Working
	case "idle":
		return agent.Idle
	case "waiting":
		return agent.Waiting
	}
	return agent.Unknown
}

// RuleArgs turns command prefixes into Bash rules. "=" keeps these variadic flags from
// taking the prompt after them as a value.
func (*Adapter) RuleArgs(allow, deny []string) []string {
	rules := func(cmds []string) string {
		var out []string
		for _, c := range cmds {
			out = append(out, "Bash("+c+":*)")
		}
		return strings.Join(out, ",")
	}
	var args []string
	if len(allow) > 0 {
		args = append(args, "--allowedTools="+rules(allow))
	}
	if len(deny) > 0 {
		args = append(args, "--disallowedTools="+rules(deny))
	}
	return args
}

// IsolateArgs: only the user's own settings load (lanes' hooks come with --settings and
// still do); the folder's .claude/settings*.json, and the hooks in them, don't.
func (*Adapter) IsolateArgs() []string { return []string{"--setting-sources=user"} }
