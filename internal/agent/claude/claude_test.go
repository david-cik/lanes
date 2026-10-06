package claude

import (
	"context"
	"os/exec"
	"regexp"
	"slices"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
)

func TestListExternal(t *testing.T) {
	a := &Adapter{run: func(context.Context) ([]byte, error) {
		return []byte(`[
		 {"pid":1,"cwd":"/src/app","kind":"interactive","startedAt":1767225600000,"sessionId":"s1","name":"abc-12 work","status":"busy"},
		 {"pid":2,"cwd":"/src","kind":"background","startedAt":1767225600000,"sessionId":"s2","name":"bg","status":"idle","id":"x1","state":"working"},
		 {"pid":3,"cwd":"/src","kind":"interactive","startedAt":0,"sessionId":"s3","name":"odd","status":"weird"},
		 {"pid":4,"cwd":"/src","kind":"interactive","startedAt":0,"sessionId":"s4","name":"ask","status":"waiting"}]`), nil
	}}
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Status != agent.Working || got[1].Status != agent.Idle ||
		got[2].Status != agent.Unknown || got[3].Status != agent.Waiting {
		t.Fatalf("got %+v", got)
	}
	if !got[0].External || got[0].ID != "s1" || got[0].Since.UnixMilli() != 1767225600000 {
		t.Fatalf("got %+v", got[0])
	}
}

func TestMissingBinaryIsEmpty(t *testing.T) {
	a := &Adapter{run: func(context.Context) ([]byte, error) { return nil, exec.ErrNotFound }}
	got, err := a.List(context.Background())
	if err != nil || got != nil {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestCommand(t *testing.T) {
	c := New().Command(agent.LaunchSpec{Name: "ABC-1 fix", Prompt: "--do it", Args: []string{"--model", "haiku"}})
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(c.SessionID) {
		t.Fatalf("session id %q", c.SessionID)
	}
	i := slices.Index(c.Argv, "claude")
	if c.Argv[0] != "env" || i < 0 || !slices.Contains(c.Argv[:i], "CLAUDE_CODE_SESSION_ID") {
		t.Fatalf("argv does not clear inherited session vars: %q", c.Argv)
	}
	want := []string{"claude", "--session-id", c.SessionID, "-n", "ABC-1 fix", "--model", "haiku", " --do it"}
	if !slices.Equal(c.Argv[i:], want) {
		t.Fatalf("argv %q, want %q", c.Argv[i:], want)
	}
	if New().Command(agent.LaunchSpec{}).SessionID == c.SessionID {
		t.Fatal("session ids repeat")
	}
}

func TestCommandFork(t *testing.T) {
	c := New().Command(agent.LaunchSpec{ResumeFrom: "orig-id", Name: "ABC-1 x"})
	i := slices.Index(c.Argv, "claude")
	want := []string{"claude", "--resume", "orig-id", "--fork-session", "--session-id", c.SessionID, "-n", "ABC-1 x"}
	if !slices.Equal(c.Argv[i:], want) {
		t.Fatalf("argv %q", c.Argv[i:])
	}
}
