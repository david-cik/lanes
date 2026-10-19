package main

import (
	"slices"
	"testing"

	"github.com/david-cik/lanes/internal/config"
)

func TestInstalledAdapters(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = map[string]config.AgentConfig{
		"zsh-agent": {Command: []string{"sh", "-c", "true"}}, // installed
		"ghost":     {Command: []string{"no-such-agent-binary"}},
		"codex":     {Command: []string{"sh"}}, // a built-in keeps its own adapter
	}
	var names []string
	for _, a := range installedAdapters(cfg) {
		names = append(names, a.Name())
	}
	if !slices.Contains(names, "zsh-agent") || slices.Contains(names, "ghost") || countOf(names, "codex") > 1 {
		t.Fatalf("%v", names)
	}
}

func countOf(s []string, v string) (n int) {
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
