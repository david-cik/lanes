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
	Ended     bool   // a linked session that is no longer running (A resumes it)
	TicketKey string // preset for launched agents; otherwise filled by matching
	RecordID  string // lanes launch record, for launched agents
	Pane      string // tmux pane, for launched agents
	Project   string // the project it coordinates, for a coordinator
	Review    string // the pull request (URL) it reviews, for a review agent
}

// LaunchSpec is what lanes asks an adapter to start.
type LaunchSpec struct {
	Name    string   // display name, e.g. "ABC-12 fix-login"
	Prompt  string   // initial prompt; may be empty
	Args    []string // extra CLI args from config
	HookBin string   // lanes executable for `lanes hook`; empty = no hooks
	// ResumeFrom forks an existing session of this tool (its conversation carries over;
	// the original keeps running). Only for adapters that implement Forker.
	ResumeFrom string
}

// Forker is implemented by adapters that can fork an existing session into a new one.
type Forker interface{ CanFork() bool }

// The interfaces below are optional too: lanes checks for each before using what it
// offers, so a tool with none of them can still be started, sent to, and stopped.

// Asker is implemented by adapters whose tool can answer one question and exit, with no
// tools and nothing saved (for the panel's ask and ticket drafting).
type Asker interface {
	Ask(ctx context.Context, system, prompt string) (string, error)
}

// MCPServer is a stdio MCP server to give one session.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
}

// Coordinates is implemented by adapters whose tool can run a project's coordinator: it
// returns the extra arguments that give one session srv, let it use srv's tools named
// in allowed without asking, and make every other tool ask first, whatever mode the
// user's own settings default to.
type Coordinates interface {
	CoordinatorArgs(srv MCPServer, allowed []string) []string
}

// Transcripts is implemented by adapters whose tool keeps a transcript of each session
// that lanes can read: to find sessions, suggest tickets for them, and read replies.
type Transcripts interface {
	// TranscriptPath finds session id's transcript (cwd, where it ran, helps if known);
	// "" if there's none.
	TranscriptPath(cwd, id string) string
	// Transcripts lists the transcripts changed since then (zero: all), by session id.
	Transcripts(since time.Time) map[string]string
	SessionInfo(path string) (Info, error)
	// Messages reads the conversation from (about) the last tail bytes of the transcript.
	Messages(path string, tail int64) []Msg
}

// Info is what a transcript says about its session.
type Info struct {
	Cwd        string
	Entrypoint string // how it was started, in the tool's words; "" = not known (yet)
	Background bool   // started by a tool or plugin, not a person: not ticket work
	First      string // the first message the user typed (usually what it's for)
	Title      string
	When       time.Time // last activity
}

// Msg is one piece of a conversation, as lanes reads any tool's transcript.
type Msg struct {
	User  bool   // the user's side (else the assistant's)
	Typed bool   // typed by a person: the user's side, and not text the tool put there
	Text  string // what it says ("" for a tool call)
	Tool  string // the input of a tool the assistant called
}

// Waiter is implemented by adapters without hooks whose tool shows known text when it
// needs the user; lanes then reads their status from their pane.
type Waiter interface {
	WaitingText() []string
}

// Ruler is implemented by adapters whose tool takes rules for shell commands: the ones
// starting with an allow prefix run without asking, a deny prefix never runs.
type Ruler interface {
	RuleArgs(allow, deny []string) []string
}

// Isolator is implemented by adapters whose tool can ignore the settings a folder brings
// with it (hooks that run commands, tools, permissions), for code someone else wrote.
type Isolator interface {
	IsolateArgs() []string
}

// Truster is implemented by adapters whose tool asks before working in a folder it
// hasn't seen.
type Truster interface {
	// TrustPrompt says whether screen (a capture of its pane) shows that question.
	TrustPrompt(screen string) bool
	// TrustKeys are the keys that answer it with yes, if screen shows the version of the
	// prompt they're for; ok is false otherwise.
	TrustKeys(screen string) (keys []string, ok bool)
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
	Writes   []string // files the tool is about to change (absolute paths)
	// SessionID is the tool's own id for the session, for a tool that can't be given one
	// at launch: lanes learns it from the hook.
	SessionID string
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
