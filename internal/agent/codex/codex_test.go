package codex

import (
	"slices"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
)

func TestCommand(t *testing.T) {
	c := New().Command(agent.LaunchSpec{Name: "ignored", Prompt: "fix it", Args: []string{"--model", "x"}})
	if want := []string{"codex", "--model", "x", "fix it"}; !slices.Equal(c.Argv, want) || c.SessionID != "" {
		t.Fatalf("got %+v", c)
	}
}
