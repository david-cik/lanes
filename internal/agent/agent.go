// Package agent models local AI coding agent sessions and the adapters that find them.
package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type Status string

const (
	Working Status = "working"
	Idle    Status = "idle"
	Waiting Status = "waiting" // needs the user: a permission prompt or question
	Unknown Status = "unknown"
)

type Agent struct {
	ID        string // tool session id
	Tool      string
	Name      string
	Cwd       string
	Branch    string
	Status    Status
	Since     time.Time
	External  bool // not launched by lanes: read-only
	TicketKey string
}

// Adapter finds sessions of one agent tool. M1 only lists externally started ones.
type Adapter interface {
	Name() string
	ListExternal(ctx context.Context) ([]Agent, error)
}

// FillBranches sets Branch from each agent's working directory (one git call per dir).
func FillBranches(ctx context.Context, agents []Agent) {
	seen := map[string]string{}
	for i := range agents {
		cwd := agents[i].Cwd
		if cwd == "" { // git -C "" would report lanes' own directory
			continue
		}
		b, ok := seen[cwd]
		if !ok {
			out, err := exec.CommandContext(ctx, "git", "-C", cwd, "branch", "--show-current").Output()
			if err == nil {
				b = strings.TrimSpace(string(out))
			}
			seen[cwd] = b
		}
		agents[i].Branch = b
	}
}
