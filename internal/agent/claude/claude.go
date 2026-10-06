// Package claude adapts Claude Code sessions.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/david-cik/lanes/internal/agent"
)

type Adapter struct {
	// run executes `claude agents --json`; tests replace it.
	run func(ctx context.Context) ([]byte, error)
}

func New() *Adapter {
	return &Adapter{run: func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	}}
}

func (*Adapter) Name() string { return "claude" }

// ListExternal lists running Claude sessions. A missing claude binary is not an error.
func (a *Adapter) ListExternal(ctx context.Context) ([]agent.Agent, error) {
	b, err := a.run(ctx)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude agents: %w", err)
	}
	var recs []struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		Cwd       string `json:"cwd"`
		Status    string `json:"status"`
		StartedAt int64  `json:"startedAt"`
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("claude agents: %w", err)
	}
	out := make([]agent.Agent, 0, len(recs))
	for _, r := range recs {
		out = append(out, agent.Agent{
			ID: r.SessionID, Tool: "claude", Name: r.Name, Cwd: r.Cwd,
			Status: status(r.Status), Since: time.UnixMilli(r.StartedAt), External: true,
		})
	}
	return out, nil
}

func status(s string) agent.Status {
	switch s {
	case "busy":
		return agent.Working
	case "idle":
		return agent.Idle
	}
	return agent.Unknown
}
