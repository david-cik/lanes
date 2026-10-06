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
	Done    Status = "done"
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
	External  bool   // not launched by lanes: read-only
	TicketKey string // preset for launched agents; otherwise filled by matching
	RecordID  string // lanes launch record, for launched agents
	Pane      string // tmux pane, for launched agents
}

// LaunchSpec is what lanes asks an adapter to start.
type LaunchSpec struct {
	Name    string   // display name, e.g. "ABC-12 fix-login"
	Prompt  string   // initial prompt; may be empty
	Args    []string // extra CLI args from config
	HookBin string   // lanes executable for `lanes hook`; empty = no hooks
}

// Command is how to run a tool in a tmux pane.
type Command struct {
	Argv      []string
	Env       []string
	SessionID string // pre-assigned tool session id, if the tool supports it
}

// Adapter knows how to find, start, and stop sessions of one agent tool.
type Adapter interface {
	Name() string
	// List returns the tool's sessions it can see, launched by lanes or not.
	List(ctx context.Context) ([]Agent, error)
	Command(spec LaunchSpec) Command
	// Stop returns text to type (then Enter) and/or tmux keys that ask the tool to exit.
	Stop() (text string, keys []string)
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

// Hooker is implemented by adapters whose tool can report events to lanes.
type Hooker interface {
	// ParseHook turns one hook payload into a status change and, for a permission
	// prompt, a request lanes can answer.
	ParseHook(event string, payload []byte) (HookEvent, error)
	// EncodeDecision renders what `lanes hook` prints back to the tool.
	EncodeDecision(d Decision) []byte
}

type HookEvent struct {
	Status   Status   // "" = no change
	Request  *Request // set when the tool asks for permission
	Activity string   // one line for the activity log; "" = nothing worth logging
}

type Request struct {
	Tool        string
	Summary     string // one line: the command, file, or URL involved
	Suggestions []Suggestion
}

// Suggestion is a permission rule the tool offers to save; lanes sends it back
// unchanged except for where to save it (and, for one-rule suggestions, its text).
type Suggestion struct {
	Label string
	Rule  string // editable rule text; "" when the suggestion isn't a single rule
	Raw   []byte
}

type Decision struct {
	Behavior string // "allow", "deny", or "" for no decision
	Message  string
	Save     *Suggestion // rule to save with the allow
	SaveTo   string      // "session" or "localSettings"
}
