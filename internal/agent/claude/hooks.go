package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
)

// hookEvents are the Claude Code hook events lanes listens to; matcher "*" where the
// event takes one. Hooks given via --settings run alongside the user's own hooks.
var hookEvents = []struct {
	name    string
	matcher bool
	timeout int
}{
	{"UserPromptSubmit", false, 10},
	{"PreToolUse", true, 10},
	{"PostToolUse", true, 10},
	{"PermissionRequest", true, 86400}, // waits for a human; the tool's own prompt shows meanwhile
	{"Notification", false, 10},
	{"Stop", false, 10},
	{"SessionEnd", false, 10},
}

// HookSettings returns the inline --settings JSON that routes events to `lanes hook`.
func HookSettings(bin string) string {
	hooks := map[string]any{}
	for _, e := range hookEvents {
		entry := map[string]any{"hooks": []map[string]any{{
			"type": "command", "command": shQuote(bin) + " hook claude " + e.name, "timeout": e.timeout,
		}}}
		if e.matcher {
			entry["matcher"] = "*"
		}
		hooks[e.name] = []any{entry}
	}
	b, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(b)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ParseHook maps a Claude hook payload to a status and, for PermissionRequest, a request.
func (*Adapter) ParseHook(event string, payload []byte) (agent.HookEvent, error) {
	switch event {
	case "UserPromptSubmit":
		var p struct {
			Prompt string `json:"prompt"`
		}
		json.Unmarshal(payload, &p)
		return agent.HookEvent{Status: agent.Working, Activity: "prompt: " + oneLine(p.Prompt)}, nil
	case "PreToolUse":
		var p struct {
			Tool  string          `json:"tool_name"`
			Input json.RawMessage `json:"tool_input"`
		}
		json.Unmarshal(payload, &p)
		return agent.HookEvent{Status: agent.Working, Activity: p.Tool + ": " + summarize(p.Input)}, nil
	case "PostToolUse":
		return agent.HookEvent{Status: agent.Working}, nil
	case "Stop":
		return agent.HookEvent{Status: agent.Idle, Activity: "turn finished"}, nil
	case "SessionEnd":
		return agent.HookEvent{Status: agent.Done, Activity: "session ended"}, nil
	case "Notification":
		var n struct {
			Type string `json:"notification_type"`
		}
		json.Unmarshal(payload, &n)
		switch n.Type {
		case "idle_prompt", "agent_completed":
			return agent.HookEvent{Status: agent.Idle}, nil
		case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
			return agent.HookEvent{Status: agent.Waiting}, nil
		}
		return agent.HookEvent{}, nil
	case "PermissionRequest":
		var p struct {
			Tool        string            `json:"tool_name"`
			Input       json.RawMessage   `json:"tool_input"`
			Suggestions []json.RawMessage `json:"permission_suggestions"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return agent.HookEvent{}, fmt.Errorf("claude PermissionRequest: %w", err)
		}
		req := &agent.Request{Tool: p.Tool, Summary: summarize(p.Input)}
		for _, raw := range p.Suggestions {
			req.Suggestions = append(req.Suggestions, suggestion(raw))
		}
		return agent.HookEvent{Status: agent.Waiting, Request: req, Activity: "asked: " + req.Tool + ": " + req.Summary}, nil
	}
	return agent.HookEvent{}, nil
}

// summarize picks the field that says what the tool will touch.
func summarize(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, k := range []string{"command", "file_path", "notebook_path", "url", "path", "pattern", "query"} {
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

// suggestion labels one of Claude's permission updates. Real payloads look like
// {"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"curl …"}],"behavior":"allow",…}
// or {"type":"addDirectories","directories":[…],…}.
func suggestion(raw json.RawMessage) agent.Suggestion {
	var s struct {
		Type  string `json:"type"`
		Rules []struct {
			Tool    string `json:"toolName"`
			Content string `json:"ruleContent"`
		} `json:"rules"`
		Dirs []string `json:"directories"`
	}
	json.Unmarshal(raw, &s)
	out := agent.Suggestion{Label: s.Type, Raw: raw}
	switch {
	case s.Type == "addRules" && len(s.Rules) > 0:
		var labels []string
		for _, r := range s.Rules {
			labels = append(labels, ruleLabel(r.Tool, r.Content))
		}
		out.Label = strings.Join(labels, ", ")
		if len(s.Rules) == 1 {
			out.Rule = s.Rules[0].Content
		}
	case s.Type == "addDirectories":
		out.Label = "access to " + strings.Join(s.Dirs, ", ")
	}
	return out
}

func ruleLabel(tool, content string) string {
	if content == "" {
		return tool
	}
	return tool + "(" + content + ")"
}

// EncodeDecision renders the PermissionRequest hook output. A suggestion being saved
// is sent back as Claude gave it, with the destination (and edited rule text) set.
func (*Adapter) EncodeDecision(d agent.Decision) []byte {
	if d.Behavior == "" {
		return nil
	}
	dec := map[string]any{"behavior": d.Behavior}
	if d.Message != "" {
		dec["message"] = d.Message
	}
	if d.Behavior == "allow" && d.Save != nil {
		var up map[string]any
		if json.Unmarshal(d.Save.Raw, &up) == nil {
			up["destination"] = d.SaveTo
			if rules, ok := up["rules"].([]any); ok && len(rules) == 1 && d.Save.Rule != "" {
				if r, ok := rules[0].(map[string]any); ok {
					r["ruleContent"] = d.Save.Rule
				}
			}
			dec["updatedPermissions"] = []any{up}
		}
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": dec,
	}})
	return b
}
