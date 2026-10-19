package generic

import (
	"slices"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
)

func TestCommandAndStop(t *testing.T) {
	a := New("aider", config.AgentConfig{Command: []string{"aider", "--yes"}, PromptFlag: "--message", Stop: "/exit"})
	got := a.Command(agent.LaunchSpec{Prompt: "-fix it", Args: []string{"--model", "x"}}).Argv
	if !slices.Equal(got, []string{"aider", "--yes", "--model", "x", "--message", " -fix it"}) {
		t.Fatalf("%q", got)
	}
	if text, keys := a.Stop(); text != "/exit" || keys != nil {
		t.Fatalf("%q %q", text, keys)
	}
	b := New("x", config.AgentConfig{Command: []string{"x"}})
	if got := b.Command(agent.LaunchSpec{Prompt: "go"}).Argv; !slices.Equal(got, []string{"x", "go"}) {
		t.Fatalf("%q", got)
	}
	if _, keys := b.Stop(); !slices.Equal(keys, []string{"C-c", "C-c"}) {
		t.Fatalf("%q", keys)
	}
}
