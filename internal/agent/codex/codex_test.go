package codex

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/david-cik/lanes/internal/agent"
)

func TestCommand(t *testing.T) {
	c := New().Command(agent.LaunchSpec{Name: "ignored", Prompt: "fix it", Args: []string{"--model", "x"}})
	if want := []string{"codex", "--model", "x", "fix it"}; !slices.Equal(c.Argv, want) || c.SessionID != "" {
		t.Fatalf("got %+v", c)
	}
}

// tomlOK checks that each -c override parses the way Codex parses it (key=TOML value).
func tomlOK(t *testing.T, args []string) map[string]any {
	t.Helper()
	all := map[string]any{}
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" {
			continue
		}
		i++
		var v map[string]any
		if err := toml.Unmarshal([]byte(args[i]), &v); err != nil {
			t.Fatalf("%q: %v", args[i], err)
		}
		merge(all, v)
	}
	return all
}

// merge adds v into all, nested tables key by key (as Codex layers -c overrides).
func merge(all, v map[string]any) {
	for k, x := range v {
		if sub, ok := x.(map[string]any); ok {
			if have, ok := all[k].(map[string]any); ok {
				merge(have, sub)
				continue
			}
		}
		all[k] = x
	}
}

func TestHookArgs(t *testing.T) {
	args := HookArgs("/opt/my lanes/lanes")
	if args[0] != "--dangerously-bypass-hook-trust" {
		t.Fatalf("%q", args)
	}
	hooks := tomlOK(t, args)["hooks"].(map[string]any)
	perm := hooks["PermissionRequest"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if perm["command"] != `'/opt/my lanes/lanes' hook codex PermissionRequest` || perm["type"] != "command" || perm["timeout"] != int64(86400) {
		t.Fatalf("%v", perm)
	}
	if len(hooks) != len(hookEvents) {
		t.Fatalf("%v", hooks)
	}
	c := New().Command(agent.LaunchSpec{Prompt: "go", HookBin: "/bin/lanes", ResumeFrom: "abc"})
	if c.Argv[0] != "codex" || c.Argv[1] != "fork" || c.Argv[len(c.Argv)-2] != "abc" || c.Argv[len(c.Argv)-1] != "go" {
		t.Fatalf("%q", c.Argv)
	}
}

func TestCoordinatorArgs(t *testing.T) {
	args := New().CoordinatorArgs(agent.MCPServer{Name: "lanes", Command: "/bin/lanes", Args: []string{"mcp"}}, []string{"agents", "wait"})
	srv := tomlOK(t, args)["mcp_servers"].(map[string]any)["lanes"].(map[string]any)
	tools := srv["tools"].(map[string]any)
	if srv["command"] != "/bin/lanes" || srv["default_tools_approval_mode"] != "prompt" ||
		tools["agents"].(map[string]any)["approval_mode"] != "approve" || tools["start"] != nil {
		t.Fatalf("%v", srv)
	}
	if !slices.Contains(args, "read-only") || !slices.Contains(args, "on-request") {
		t.Fatalf("%q", args)
	}
}

func TestParseHook(t *testing.T) {
	a := New()
	he, _ := a.ParseHook("PermissionRequest", []byte(`{"session_id":"s-1","cwd":"/w","tool_name":"Bash","tool_input":{"command":"npm   test"}}`))
	if he.Status != agent.Waiting || he.Request == nil || he.Request.Summary != "npm test" || he.SessionID != "s-1" {
		t.Fatalf("%+v", he)
	}
	he, _ = a.ParseHook("PreToolUse", []byte(`{"cwd":"/w","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Update File: src/a.go\n*** Add File: /abs/b.go\n*** End Patch"}}`))
	if !slices.Equal(he.Writes, []string{"/w/src/a.go", "/abs/b.go"}) || he.Activity != "apply_patch: edit files" {
		t.Fatalf("%+v", he)
	}
	for ev, want := range map[string]agent.Status{"UserPromptSubmit": agent.Working, "Stop": agent.Idle, "SessionEnd": agent.Done} {
		if he, _ := a.ParseHook(ev, []byte(`{}`)); he.Status != want {
			t.Fatalf("%s: %+v", ev, he)
		}
	}
	got := string(a.EncodeDecision(agent.Decision{Behavior: "deny", Message: "no", Save: &agent.Suggestion{Label: "x"}}))
	if got != `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"no"},"hookEventName":"PermissionRequest"}}` {
		t.Fatalf("%s", got)
	}
}

// The lines below have the shape Codex 0.163 wrote (trimmed).
func TestTranscripts(t *testing.T) {
	h := t.TempDir()
	t.Setenv("CODEX_HOME", h)
	id := "01a1566b-a18b-7571-9bf6-5e163e8ed5dc"
	dir := filepath.Join(h, "sessions", "2026", "10", "19")
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "rollout-2026-10-19T18-07-36-"+id+".jsonl")
	os.WriteFile(p, []byte(strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + id + `","cwd":"/w/app","originator":"codex-tui","source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules about ABC-9"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>ABC-9</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix ABC-1"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"on it"}]}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"git switch abc-1/x"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
	}, "\n")+"\n"), 0o600)
	os.WriteFile(filepath.Join(h, "session_index.jsonl"), []byte(`{"id":"`+id+`","thread_name":"Old"}`+"\n"+`{"id":"other","thread_name":"No"}`+"\n"+`{"id":"`+id+`","thread_name":"Fix login"}`+"\n"), 0o600)
	a := New()
	if got := a.TranscriptPath("", id); got != p || a.Transcripts(time.Time{})[id] != p {
		t.Fatalf("%q", got)
	}
	in, err := a.SessionInfo(p)
	if err != nil || in.Cwd != "/w/app" || in.First != "fix ABC-1" || in.Title != "Fix login" || in.Background {
		t.Fatalf("%+v %v", in, err)
	}
	msgs := a.Messages(p, 1<<20)
	if len(msgs) != 5 || msgs[0].Typed || !msgs[1].Typed || msgs[2].Text != "on it" || msgs[3].Tool != "git switch abc-1/x" || msgs[4].Tool != `{"cmd":"ls"}` {
		t.Fatalf("%+v", msgs)
	}
	if a.TranscriptPath("", "short") != "" {
		t.Fatal("not a session id")
	}
}

func TestTrustKeys(t *testing.T) {
	screen := "  Trust this folder? Codex can read, edit, and run files here…\n› 1. Trust and continue\n  2. Quit\n"
	a := New()
	if keys, ok := a.TrustKeys(screen); !a.TrustPrompt(screen) || !ok || !slices.Equal(keys, []string{"Enter"}) {
		t.Fatalf("%q %v", keys, ok)
	}
	if _, ok := a.TrustKeys(strings.Replace(screen, "› 1.", "  1.", 1)); ok {
		t.Fatal("answered with Trust not highlighted")
	}
}
