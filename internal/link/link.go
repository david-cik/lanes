// Package link matches agents to ticket keys.
package link

import (
	"regexp"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/tracker"
)

var keyRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]*)-(\d+)\b`)

// Prefixes returns the lower-case team prefixes seen in issue keys (ABC-12 → abc).
func Prefixes(issues []tracker.Issue) map[string]bool {
	p := map[string]bool{}
	for _, i := range issues {
		if pre, _, ok := strings.Cut(i.Key, "-"); ok {
			p[strings.ToLower(pre)] = true
		}
	}
	return p
}

// Match returns the first known ticket key in the agent's branch, then name, then cwd.
func Match(a agent.Agent, prefixes map[string]bool) string {
	for _, s := range []string{a.Branch, a.Name, a.Cwd} {
		for _, m := range keyRe.FindAllStringSubmatch(s, -1) {
			if prefixes[strings.ToLower(m[1])] {
				return strings.ToUpper(m[1]) + "-" + m[2]
			}
		}
	}
	return ""
}
