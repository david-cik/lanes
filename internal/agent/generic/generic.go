// Package generic runs an agent tool lanes has no built-in support for, as configured
// in [agents.<name>]: lanes can start, send to, and stop it, and tell from its pane
// whether it's working, idle, or waiting for you.
package generic

import (
	"context"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
)

type Adapter struct {
	name string
	cfg  config.AgentConfig
}

func New(name string, cfg config.AgentConfig) *Adapter { return &Adapter{name, cfg} }

func (a *Adapter) Name() string { return a.name }

// Binary is the program the tool runs, to check that it's installed.
func (a *Adapter) Binary() string { return a.cfg.Command[0] }

// List: sessions lanes didn't start aren't visible for a tool lanes doesn't know.
func (*Adapter) List(context.Context) ([]agent.Agent, error) { return nil, nil }

func (a *Adapter) Command(spec agent.LaunchSpec) agent.Command {
	argv := append(append([]string{}, a.cfg.Command...), spec.Args...)
	if spec.Prompt != "" {
		if a.cfg.PromptFlag != "" {
			argv = append(argv, a.cfg.PromptFlag)
		}
		p := spec.Prompt
		if strings.HasPrefix(p, "-") { // don't let a prompt be parsed as a flag
			p = " " + p
		}
		argv = append(argv, p)
	}
	return agent.Command{Argv: argv}
}

func (a *Adapter) Stop() (string, []string) {
	if a.cfg.Stop == "" && len(a.cfg.StopKeys) == 0 {
		return "", []string{"C-c", "C-c"}
	}
	return a.cfg.Stop, a.cfg.StopKeys
}

// WaitingText is what its pane shows when it needs you (see 1c, pane status).
func (a *Adapter) WaitingText() []string { return a.cfg.Waiting }
