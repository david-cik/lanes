// Package codex adapts the OpenAI Codex CLI. It has no session listing lanes can use
// yet, so only sessions lanes launched are shown (status from the tmux pane).
package codex

import (
	"context"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Name() string { return "codex" }

func (*Adapter) List(context.Context) ([]agent.Agent, error) { return nil, nil }

func (*Adapter) Command(spec agent.LaunchSpec) agent.Command {
	argv := append([]string{"codex"}, spec.Args...)
	if spec.Prompt != "" {
		p := spec.Prompt
		if strings.HasPrefix(p, "-") {
			p = " " + p
		}
		argv = append(argv, p)
	}
	return agent.Command{Argv: argv}
}

// ponytail: unverified until codex is installed; Ctrl-C twice is the documented quit.
func (*Adapter) Stop() (string, []string) { return "", []string{"C-c", "C-c"} }
