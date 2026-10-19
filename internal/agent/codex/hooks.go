package codex

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
)

// Codex's hooks are close to Claude Code's: JSON on stdin with session_id, cwd, and the
// event; PermissionRequest can answer with a decision. lanes passes them for each
// session it starts with -c (see HookArgs).

// hookEvents are the events lanes listens to, with how long Codex waits for each.
var hookEvents = []struct {
	name    string
	timeout int
}{
	{"SessionStart", 10},
	{"UserPromptSubmit", 10},
	{"PreToolUse", 10},
	{"PostToolUse", 10},
	{"PermissionRequest", 86400}, // waits for a human; Codex's own prompt shows meanwhile
	{"Stop", 10},
	{"SessionEnd", 3}, // Codex allows at most 3s here
}

// HookArgs are the arguments that make a Codex session report each event to
// `bin hook codex <event>`. Codex runs only hooks you've trusted in its /hooks screen,
// so these come with --dangerously-bypass-hook-trust: for that session it also runs
// any hooks you've configured but never trusted.
func HookArgs(bin string) []string {
	args := []string{"--dangerously-bypass-hook-trust"}
	for _, e := range hookEvents {
		cmd := shQuote(bin) + " hook codex " + e.name
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			e.name, strconv.Quote(cmd), e.timeout))
	}
	return args
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (*Adapter) ParseHook(event string, payload []byte) (agent.HookEvent, error) {
	var p struct {
		Session string          `json:"session_id"`
		Cwd     string          `json:"cwd"`
		Prompt  string          `json:"prompt"`
		Tool    string          `json:"tool_name"`
		Input   json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &p); err != nil && event == "PermissionRequest" {
		return agent.HookEvent{}, fmt.Errorf("codex PermissionRequest: %w", err)
	}
	var he agent.HookEvent
	switch event {
	case "SessionStart":
		he = agent.HookEvent{Status: agent.Idle}
	case "UserPromptSubmit":
		he = agent.HookEvent{Status: agent.Working, Activity: "prompt: " + oneLine(p.Prompt)}
	case "PreToolUse":
		he = agent.HookEvent{Status: agent.Working, Activity: p.Tool + ": " + summarize(p.Input), Writes: writes(p.Tool, p.Input, p.Cwd)}
	case "PostToolUse":
		he = agent.HookEvent{Status: agent.Working}
	case "Stop":
		he = agent.HookEvent{Status: agent.Idle, Activity: "turn finished"}
	case "SessionEnd":
		he = agent.HookEvent{Status: agent.Done, Activity: "session ended"}
	case "PermissionRequest":
		req := &agent.Request{Tool: p.Tool, Summary: summarize(p.Input)} // Codex offers no rules to save
		he = agent.HookEvent{Status: agent.Waiting, Request: req, Activity: "asked: " + req.Tool + ": " + req.Summary}
	}
	he.SessionID = p.Session
	return he, nil
}

// EncodeDecision answers a PermissionRequest. Codex refuses (fails closed on) anything
// beyond allow/deny and a message, so a rule to save is never sent.
func (*Adapter) EncodeDecision(d agent.Decision) []byte {
	dec := map[string]any{"behavior": d.Behavior}
	if d.Message != "" {
		dec["message"] = d.Message
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": dec}})
	return b
}

// writes is what an apply_patch is about to change: the files its patch names.
func writes(tool string, input json.RawMessage, cwd string) []string {
	if tool != "apply_patch" {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	json.Unmarshal(input, &in)
	var out []string
	for _, l := range strings.Split(in.Command, "\n") {
		for _, p := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if f, ok := strings.CutPrefix(l, p); ok && strings.TrimSpace(f) != "" {
				f = strings.TrimSpace(f)
				if !filepath.IsAbs(f) && cwd != "" {
					f = filepath.Join(cwd, f)
				}
				out = append(out, f)
			}
		}
	}
	return out
}

// summarize picks what the tool will touch: a command, else its input.
func summarize(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, k := range []string{"command", "description", "path", "url"} {
			if v, ok := m[k].(string); ok && v != "" {
				if k == "command" && strings.HasPrefix(v, "*** Begin Patch") {
					return "edit files"
				}
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
