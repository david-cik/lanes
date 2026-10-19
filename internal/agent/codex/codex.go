// Package codex adapts the OpenAI Codex CLI. Codex can't list its running sessions, so
// only sessions lanes launched are on the board; their status, permission prompts, and
// session ids come from Codex's hooks (hooks.go).
package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Name() string { return "codex" }

func (*Adapter) List(context.Context) ([]agent.Agent, error) { return nil, nil }

func (*Adapter) Command(spec agent.LaunchSpec) agent.Command {
	argv := []string{"codex"}
	if spec.ResumeFrom != "" {
		argv = append(argv, "fork")
	}
	if spec.HookBin != "" {
		argv = append(argv, HookArgs(spec.HookBin)...)
	}
	argv = append(argv, spec.Args...)
	if spec.ResumeFrom != "" {
		argv = append(argv, spec.ResumeFrom)
	}
	if spec.Prompt != "" {
		p := spec.Prompt
		if strings.HasPrefix(p, "-") { // don't let a prompt be parsed as a flag
			p = " " + p
		}
		argv = append(argv, p)
	}
	return agent.Command{Argv: argv}
}

func (*Adapter) Stop() (string, []string) { return "/quit", nil }

func (*Adapter) CanFork() bool { return true }

// Ask runs one `codex exec` that can't change anything and isn't saved, and returns its
// last message. Codex has no separate system prompt, so system goes first.
func (*Adapter) Ask(ctx context.Context, system, prompt string) (string, error) {
	out, err := os.CreateTemp("", "lanes-codex-*.txt")
	if err != nil {
		return "", err
	}
	out.Close()
	defer os.Remove(out.Name())
	cmd := exec.CommandContext(ctx, "codex", "exec", "--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check",
		"--color", "never", "-o", out.Name(), system+"\n\n"+prompt)
	cmd.Stdin = nil
	if b, err := cmd.CombinedOutput(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
		return "", fmt.Errorf("codex: %w: %s", err, line)
	}
	b, err := os.ReadFile(out.Name())
	return strings.TrimSpace(string(b)), err
}

// CoordinatorArgs gives the session srv with the allowed tools approved in advance and
// every other tool of srv asking first; read-only, since a coordinator changes nothing
// itself, and asking for approval even if the user's config says never.
func (*Adapter) CoordinatorArgs(srv agent.MCPServer, allowed []string) []string {
	var args []string
	set := func(k, v string) { args = append(args, "-c", "mcp_servers."+srv.Name+"."+k+"="+v) }
	set("command", strconv.Quote(srv.Command))
	var quoted []string
	for _, a := range srv.Args {
		quoted = append(quoted, strconv.Quote(a))
	}
	set("args", "["+strings.Join(quoted, ",")+"]")
	set("default_tools_approval_mode", `"prompt"`)
	for _, t := range allowed {
		set("tools."+t+".approval_mode", `"approve"`)
	}
	return append(args, "--sandbox", "read-only", "--ask-for-approval", "on-request")
}

func (*Adapter) TrustPrompt(screen string) bool {
	return strings.Contains(screen, "Trust this folder?")
}

// TrustKeys: "1. Trust and continue" is the first choice, highlighted at the start (seen
// for a new git worktree; Codex doesn't ask in every folder).
func (*Adapter) TrustKeys(screen string) ([]string, bool) {
	if !strings.Contains(screen, "› 1. Trust and continue") {
		return nil, false
	}
	return []string{"Enter"}, true
}
