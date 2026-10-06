package link

import (
	"testing"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/tracker"
)

func TestMatch(t *testing.T) {
	pre := Prefixes([]tracker.Issue{{Key: "ABC-1"}, {Key: "XY2-7"}})
	cases := []struct {
		name string
		a    agent.Agent
		want string
	}{
		{"tracker branch", agent.Agent{Branch: "abc-12-fix-thing"}, "ABC-12"},
		{"user-prefixed branch", agent.Agent{Branch: "someone/abc-12-fix"}, "ABC-12"},
		{"slash branch", agent.Agent{Branch: "abc-12/fix"}, "ABC-12"},
		{"digit in prefix", agent.Agent{Branch: "xy2-7-x"}, "XY2-7"},
		{"name fallback", agent.Agent{Branch: "main", Name: "work on ABC-3"}, "ABC-3"},
		{"cwd fallback", agent.Agent{Cwd: "/src/app/.worktrees/abc-4/fix"}, "ABC-4"},
		{"branch beats name", agent.Agent{Branch: "abc-5-x", Name: "ABC-6"}, "ABC-5"},
		{"unknown prefix ignored", agent.Agent{Branch: "release-2026"}, ""},
		{"unknown then known", agent.Agent{Branch: "release-2026-abc-8"}, "ABC-8"},
		{"underscore separator", agent.Agent{Branch: "feat_abc-13"}, "ABC-13"},
		{"zero padded", agent.Agent{Branch: "abc-012-x"}, "ABC-12"},
		{"no match", agent.Agent{Branch: "main", Name: "scratch", Cwd: "/src"}, ""},
	}
	for _, c := range cases {
		if got := Match(c.a, pre); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
