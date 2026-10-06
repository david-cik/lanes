package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
)

// Payload shapes recorded from Claude Code 2.1.x (values made up).
const permissionPayload = `{"session_id":"s","cwd":"/src/app","hook_event_name":"PermissionRequest",
 "tool_name":"Bash","tool_input":{"command":"curl -sI https://example.com","description":"x"},
 "permission_suggestions":[
  {"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"curl -sI https://example.com"}],"behavior":"allow","destination":"localSettings"},
  {"type":"addDirectories","directories":["/src/app"],"destination":"session"}]}`

func TestParsePermissionRequest(t *testing.T) {
	ev, err := New().ParseHook("PermissionRequest", []byte(permissionPayload))
	if err != nil {
		t.Fatal(err)
	}
	r := ev.Request
	if ev.Status != agent.Waiting || r.Tool != "Bash" || r.Summary != "curl -sI https://example.com" || len(r.Suggestions) != 2 {
		t.Fatalf("got %+v %+v", ev, r)
	}
	if s := r.Suggestions[0]; s.Label != "Bash(curl -sI https://example.com)" || s.Rule != "curl -sI https://example.com" {
		t.Fatalf("suggestion %+v", s)
	}
	if s := r.Suggestions[1]; s.Label != "access to /src/app" || s.Rule != "" {
		t.Fatalf("suggestion %+v", s)
	}
}

func TestParseStatusEvents(t *testing.T) {
	cases := map[string]agent.Status{
		"UserPromptSubmit": agent.Working, "PreToolUse": agent.Working, "Stop": agent.Idle, "SessionEnd": agent.Done,
	}
	for ev, want := range cases {
		got, _ := New().ParseHook(ev, []byte(`{}`))
		if got.Status != want {
			t.Errorf("%s: %q", ev, got.Status)
		}
	}
	n, _ := New().ParseHook("Notification", []byte(`{"notification_type":"permission_prompt"}`))
	i, _ := New().ParseHook("Notification", []byte(`{"notification_type":"idle_prompt"}`))
	o, _ := New().ParseHook("Notification", []byte(`{"notification_type":"auth_success"}`))
	if n.Status != agent.Waiting || i.Status != agent.Idle || o.Status != "" {
		t.Fatalf("%q %q %q", n.Status, i.Status, o.Status)
	}
}

func decision(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out struct {
		H struct {
			Event    string         `json:"hookEventName"`
			Decision map[string]any `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.H.Event != "PermissionRequest" {
		t.Fatalf("bad output %s: %v", b, err)
	}
	return out.H.Decision
}

func TestEncodeDecision(t *testing.T) {
	a := New()
	ev, _ := a.ParseHook("PermissionRequest", []byte(permissionPayload))
	s := ev.Request.Suggestions[0]

	if b := a.EncodeDecision(agent.Decision{}); b != nil {
		t.Fatalf("no decision must print nothing, got %s", b)
	}
	if d := decision(t, a.EncodeDecision(agent.Decision{Behavior: "allow"})); d["behavior"] != "allow" || d["updatedPermissions"] != nil {
		t.Fatalf("allow once: %v", d)
	}
	d := decision(t, a.EncodeDecision(agent.Decision{Behavior: "deny", Message: "use the staging bucket"}))
	if d["behavior"] != "deny" || d["message"] != "use the staging bucket" {
		t.Fatalf("deny: %v", d)
	}

	s.Rule = "curl -sI *" // edited
	d = decision(t, a.EncodeDecision(agent.Decision{Behavior: "allow", Save: &s, SaveTo: "session"}))
	ups := d["updatedPermissions"].([]any)
	up := ups[0].(map[string]any)
	rule := up["rules"].([]any)[0].(map[string]any)
	if up["destination"] != "session" || up["type"] != "addRules" || rule["ruleContent"] != "curl -sI *" || rule["toolName"] != "Bash" {
		t.Fatalf("save: %v", up)
	}
}

func TestHookSettings(t *testing.T) {
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(HookSettings("/opt/my lanes/lanes")), &s); err != nil {
		t.Fatal(err)
	}
	pr := s.Hooks["PermissionRequest"][0]
	if pr.Matcher != "*" || pr.Hooks[0].Command != `'/opt/my lanes/lanes' hook claude PermissionRequest` || pr.Hooks[0].Timeout != 86400 {
		t.Fatalf("PermissionRequest hook %+v", pr)
	}
	if len(s.Hooks) != 7 || s.Hooks["Stop"][0].Matcher != "" {
		t.Fatalf("hooks %+v", s.Hooks)
	}
	argv := New().Command(agent.LaunchSpec{HookBin: "/x/lanes"}).Argv
	if i := strings.Index(strings.Join(argv, "\x00"), "--settings"); i < 0 {
		t.Fatalf("argv lacks --settings: %q", argv)
	}
}

func TestActivityLines(t *testing.T) {
	a := New()
	cases := map[string][2]string{
		"pre":    {"PreToolUse", `{"tool_name":"Edit","tool_input":{"file_path":"internal/x.go","old_string":"a"}}`},
		"prompt": {"UserPromptSubmit", `{"prompt":"fix the\nlogin bug"}`},
		"stop":   {"Stop", `{}`},
		"post":   {"PostToolUse", `{"tool_name":"Edit"}`},
	}
	want := map[string]string{"pre": "Edit: internal/x.go", "prompt": "prompt: fix the login bug", "stop": "turn finished", "post": ""}
	for name, c := range cases {
		ev, _ := a.ParseHook(c[0], []byte(c[1]))
		if ev.Activity != want[name] {
			t.Errorf("%s: %q, want %q", name, ev.Activity, want[name])
		}
	}
}
