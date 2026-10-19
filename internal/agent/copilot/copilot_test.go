package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/david-cik/lanes/internal/agent"
)

func TestCommandWritesTheHooksPlugin(t *testing.T) {
	dir := t.TempDir()
	c := New().WithPluginDir(dir).Command(agent.LaunchSpec{Prompt: "fix it", Args: []string{"--model", "x"}, HookBin: "/opt/my lanes/lanes"})
	want := []string{"copilot", "--session-id", c.SessionID, "--plugin-dir", dir, "--model", "x", "-i", "fix it"}
	if !slices.Equal(c.Argv, want) || len(c.SessionID) != 36 {
		t.Fatalf("%q", c.Argv)
	}
	var hooks struct {
		Version int
		Hooks   map[string][]struct {
			Exec string
			Args []string
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err := json.Unmarshal(b, &hooks); err != nil || hooks.Version != 1 || len(hooks.Hooks) != len(hookEvents) {
		t.Fatalf("%s %v", b, err)
	}
	if h := hooks.Hooks["Stop"][0]; h.Exec != "/opt/my lanes/lanes" || !slices.Equal(h.Args, []string{"hook", "copilot", "Stop"}) {
		t.Fatalf("%+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatal(err)
	}
	if c := New().Command(agent.LaunchSpec{HookBin: "/bin/lanes"}); slices.Contains(c.Argv, "--plugin-dir") || slices.Contains(c.Argv, "-i") { // no plugin folder: no hooks
		t.Fatalf("%q", c.Argv)
	}
}

// The payloads below are as Copilot CLI 1.0.100 sent them.
func TestParseHook(t *testing.T) {
	a := New()
	he, _ := a.ParseHook("Notification", []byte(`{"sessionId":"s1","cwd":"/w","message":"Run command: touch probe.txt","title":"Permission needed","hook_event_name":"Notification","notification_type":"permission_prompt"}`))
	if he.Status != agent.Waiting || he.Request != nil || he.SessionID != "s1" || !strings.Contains(he.Activity, "touch probe.txt") {
		t.Fatalf("%+v", he)
	}
	he, _ = a.ParseHook("PreToolUse", []byte(`{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w","tool_name":"Bash","tool_input":{"command":"echo hi","description":"Echo hi"}}`))
	if he.Status != agent.Working || he.Activity != "Bash: echo hi" || he.SessionID != "s1" {
		t.Fatalf("%+v", he)
	}
	if he, _ := a.ParseHook("Notification", []byte(`{"notification_type":"shell_completed"}`)); he.Status != "" {
		t.Fatalf("background shell isn't news: %+v", he)
	}
	for ev, want := range map[string]agent.Status{"UserPromptSubmit": agent.Working, "Stop": agent.Idle, "SessionEnd": agent.Done, "SessionStart": agent.Idle} {
		if he, _ := a.ParseHook(ev, []byte(`{}`)); he.Status != want {
			t.Fatalf("%s: %+v", ev, he)
		}
	}
}

func TestCoordinatorArgs(t *testing.T) {
	args := New().CoordinatorArgs(agent.MCPServer{Name: "lanes", Command: "/bin/lanes", Args: []string{"mcp"}}, []string{"agents", "wait"})
	if !slices.Equal(args[1:], []string{"--allow-tool=lanes(agents)", "--allow-tool=lanes(wait)"}) {
		t.Fatalf("%q", args)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type, Command string
			Args, Tools   []string
		}
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[0], "--additional-mcp-config=")), &cfg); err != nil || cfg.MCPServers["lanes"].Command != "/bin/lanes" || cfg.MCPServers["lanes"].Type != "local" {
		t.Fatalf("%s %v", args[0], err)
	}
}

func TestTrust(t *testing.T) {
	screen := "│ Confirm folder trust │\n│ Do you trust the files in this folder? │\n│ ❯ 1. Yes │\n│   2. Yes, and remember this folder for future sessions │\n│   3. No (Esc) │"
	a := New()
	if !a.TrustPrompt(screen) {
		t.Fatal("not spotted")
	}
	if keys, ok := a.TrustKeys(screen); !ok || !slices.Equal(keys, []string{"Enter"}) {
		t.Fatalf("%q %v", keys, ok)
	}
	if _, ok := a.TrustKeys(strings.Replace(screen, "❯ 1. Yes", "  1. Yes", 1)); ok {
		t.Fatal("answered with Yes not highlighted")
	}
}

func TestTranscripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	dir := filepath.Join(home, "session-state", "s1")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"s1","context":{"cwd":"/w/app"}}}`,
		`{"type":"user.message","data":{"content":"fix ABC-1 please"}}`,
		`{"type":"assistant.turn_start","data":{"turnId":"0"}}`,
		`{"type":"assistant.message","data":{"content":"","toolRequests":[{"name":"bash","arguments":{"command":"git switch abc-1/x"}}]}}`,
		`{"type":"assistant.message","data":{"content":"done with ABC-1"}}`,
	}, "\n")), 0o600)
	os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("id: s1\nname: Fix the login\n"), 0o600)
	a := New()
	p := a.TranscriptPath("", "s1")
	if p == "" || a.Transcripts(time.Time{})["s1"] != p {
		t.Fatalf("%q", p)
	}
	in, err := a.SessionInfo(p)
	if err != nil || in.Cwd != "/w/app" || in.First != "fix ABC-1 please" || in.Title != "Fix the login" || in.Background {
		t.Fatalf("%+v %v", in, err)
	}
	msgs := a.Messages(p, 1<<20)
	if len(msgs) != 3 || !msgs[0].Typed || msgs[1].Tool != `{"command":"git switch abc-1/x"}` || msgs[2].Text != "done with ABC-1" {
		t.Fatalf("%+v", msgs)
	}
	os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"session.start","data":{"context":{"cwd":"/w"}}}`+"\n"+
		`{"type":"user.message","data":{"content":"x"}}`+"\n"+`{"type":"system.message","data":{"role":"system","content":"You are running in non-interactive mode"}}`+"\n"), 0o600)
	if in, _ := a.SessionInfo(p); !in.Background {
		t.Fatalf("a -p session isn't a person's: %+v", in)
	}
}
