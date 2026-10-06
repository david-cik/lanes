// Package claude adapts Claude Code sessions.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

// List lists running Claude sessions. A missing claude binary is not an error.
func (a *Adapter) List(ctx context.Context) ([]agent.Agent, error) {
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

// inherited are variables that tie a process to a running Claude session. If lanes
// itself was started from inside Claude, a launched agent would inherit them and be
// treated as that session's child (taking its name, among other things).
var inherited = []string{
	"CLAUDECODE", "CLAUDE_PID", "CLAUDE_JOB_DIR", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_CODE_BRIDGE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT",
}

// Command starts an interactive session with a pre-assigned id so lanes can find it
// again in `claude agents --json`.
func (*Adapter) Command(spec agent.LaunchSpec) agent.Command {
	id := uuid()
	argv := []string{"env"}
	for _, v := range inherited {
		argv = append(argv, "-u", v)
	}
	argv = append(argv, "claude", "--session-id", id)
	if spec.HookBin != "" {
		argv = append(argv, "--settings", HookSettings(spec.HookBin))
	}
	if spec.Name != "" {
		argv = append(argv, "-n", spec.Name)
	}
	argv = append(argv, spec.Args...)
	if spec.Prompt != "" {
		p := spec.Prompt
		if strings.HasPrefix(p, "-") { // don't let a prompt be parsed as a flag
			p = " " + p
		}
		argv = append(argv, p)
	}
	return agent.Command{Argv: argv, SessionID: id}
}

func (*Adapter) Stop() (string, []string) { return "/exit", nil }

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func status(s string) agent.Status {
	switch s {
	case "busy":
		return agent.Working
	case "idle":
		return agent.Idle
	case "waiting":
		return agent.Waiting
	}
	return agent.Unknown
}
