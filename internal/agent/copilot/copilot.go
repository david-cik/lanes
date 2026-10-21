// Package copilot adapts the GitHub Copilot CLI. Copilot can't list its running
// sessions, so only sessions lanes launched are on the board. Their status comes from
// hooks lanes loads as a plugin for each session (--plugin-dir); a permission prompt
// shows as needing you, to answer in the agent's pane: Copilot's permissionRequest hook
// runs before its own rules, so answering there would hold up every tool call.
package copilot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
)

type Adapter struct {
	pluginDir string // where lanes writes its hooks plugin; "" = no hooks
}

func New() *Adapter { return &Adapter{} }

// WithPluginDir puts the hooks plugin in dir (lanes' state directory).
func (a *Adapter) WithPluginDir(dir string) *Adapter { a.pluginDir = dir; return a }

func (*Adapter) Name() string { return "copilot" }

func (*Adapter) List(context.Context) ([]agent.Agent, error) { return nil, nil }

func (a *Adapter) Command(spec agent.LaunchSpec) agent.Command {
	id := uuid()
	argv := []string{"copilot", "--session-id", id}
	if spec.HookBin != "" && a.pluginDir != "" {
		if dir, err := a.plugin(spec.HookBin); err == nil {
			argv = append(argv, "--plugin-dir", dir)
		}
	}
	argv = append(argv, spec.Args...)
	if spec.Prompt != "" {
		argv = append(argv, "-i", spec.Prompt)
	}
	return agent.Command{Argv: argv, SessionID: id}
}

func (*Adapter) Stop() (string, []string) { return "/exit", nil }

// hookEvents are the events lanes listens to. PascalCase names get Claude-style payloads
// (session_id, tool_name, tool_input).
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd"}

// plugin writes the plugin that routes Copilot's hooks to `bin hook copilot <event>`,
// and returns its folder. Written on each launch: the lanes binary may have moved.
func (a *Adapter) plugin(bin string) (string, error) {
	dir := a.pluginDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	hooks := map[string]any{}
	for _, e := range hookEvents {
		hooks[e] = []map[string]any{{"type": "command", "exec": bin, "args": []string{"hook", "copilot", e}, "timeoutSec": 10}}
	}
	manifest, _ := json.Marshal(map[string]any{"name": "lanes", "version": "1.0.0",
		"description": "Reports this session's status to the lanes panel."})
	b, _ := json.MarshalIndent(map[string]any{"version": 1, "hooks": hooks}, "", "  ")
	if err := writeAtomic(filepath.Join(dir, "plugin.json"), manifest); err != nil {
		return "", err
	}
	return dir, writeAtomic(filepath.Join(dir, "hooks.json"), b)
}

// writeAtomic replaces path whole, so a session starting meanwhile never reads half of it.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (*Adapter) ParseHook(event string, payload []byte) (agent.HookEvent, error) {
	var p struct {
		Session  string          `json:"session_id"`
		Session2 string          `json:"sessionId"` // Notification's payload is camelCase
		Cwd      string          `json:"cwd"`
		Prompt   string          `json:"prompt"`
		Tool     string          `json:"tool_name"`
		Input    json.RawMessage `json:"tool_input"`
		Type     string          `json:"notification_type"`
		Message  string          `json:"message"`
	}
	json.Unmarshal(payload, &p)
	var he agent.HookEvent
	switch event {
	case "SessionStart":
		he = agent.HookEvent{Status: agent.Idle}
	case "UserPromptSubmit":
		he = agent.HookEvent{Status: agent.Working, Activity: "prompt: " + oneLine(p.Prompt)}
	case "PreToolUse":
		he = agent.HookEvent{Status: agent.Working, Activity: p.Tool + ": " + summarize(p.Input), Writes: writes(p.Input, p.Cwd)}
	case "PostToolUse":
		he = agent.HookEvent{Status: agent.Working}
	case "Notification":
		if p.Type == "permission_prompt" || p.Type == "elicitation_dialog" {
			he = agent.HookEvent{Status: agent.Waiting, Activity: "asked: " + oneLine(p.Message)}
		}
	case "Stop":
		he = agent.HookEvent{Status: agent.Idle, Activity: "turn finished"}
	case "SessionEnd":
		he = agent.HookEvent{Status: agent.Done, Activity: "session ended"}
	}
	he.SessionID = p.Session + p.Session2
	return he, nil
}

// EncodeDecision: lanes doesn't answer Copilot's prompts (see the package comment).
func (*Adapter) EncodeDecision(agent.Decision) []byte { return nil }

// Ask runs one `copilot -p` with no tools, and returns its answer. Copilot has no
// separate system prompt, so system goes first.
func (*Adapter) Ask(ctx context.Context, system, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "copilot", "-p", system+"\n\n"+prompt, "-s", "--available-tools", "--no-custom-instructions")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("copilot: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CoordinatorArgs gives the session srv with the allowed tools approved in advance;
// Copilot asks before every other tool unless told otherwise. "=" keeps the variadic
// --allow-tool from taking what follows.
func (*Adapter) CoordinatorArgs(srv agent.MCPServer, allowed []string) []string {
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{srv.Name: map[string]any{
		"type": "local", "command": srv.Command, "args": srv.Args, "tools": []string{"*"}}}})
	args := []string{"--additional-mcp-config=" + string(cfg)}
	for _, t := range allowed {
		args = append(args, "--allow-tool="+srv.Name+"("+t+")")
	}
	return args
}

func (*Adapter) TrustPrompt(screen string) bool {
	return strings.Contains(screen, "Confirm folder trust") || strings.Contains(screen, "Do you trust the files in this folder?")
}

// TrustKeys: "1. Yes" (for this session) is the first choice, highlighted at the start.
func (*Adapter) TrustKeys(screen string) ([]string, bool) {
	if !strings.Contains(screen, "❯ 1. Yes") {
		return nil, false
	}
	return []string{"Enter"}, true
}

// writes is the file an edit or create is about to change.
func writes(input json.RawMessage, cwd string) []string {
	var in struct {
		Path string `json:"path"`
		File string `json:"file_path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path+in.File == "" {
		return nil
	}
	f := in.Path + in.File
	if !filepath.IsAbs(f) && cwd != "" {
		f = filepath.Join(cwd, f)
	}
	return []string{f}
}

func summarize(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, k := range []string{"command", "path", "file_path", "url", "pattern", "query"} {
			if v, ok := m[k].(string); ok && v != "" {
				return oneLine(v)
			}
		}
	}
	return oneLine(string(input))
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RuleArgs turns command prefixes into shell rules ("=" keeps the variadic flags from
// taking what follows).
func (*Adapter) RuleArgs(allow, deny []string) []string {
	var args []string
	for _, c := range allow {
		args = append(args, "--allow-tool=shell("+c+":*)")
	}
	for _, c := range deny {
		args = append(args, "--deny-tool=shell("+c+":*)")
	}
	return args
}
