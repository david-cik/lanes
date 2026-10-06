// Command lanes is a terminal board of tracker tickets and the local AI agents working them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/agent/codex"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/fleet"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/install"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
	"github.com/david-cik/lanes/internal/tracker/linear"
	"github.com/david-cik/lanes/internal/ui"
)

const tmuxSession = "lanes"

func main() {
	// `lanes hook <tool> <event>` runs inside agent tools on every hook event: keep it
	// fast (no config, no network) and silent on any failure.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		socket := os.Getenv("LANES_SOCKET")
		switch {
		case len(os.Args) == 4:
			hook.Client(os.Stdin, os.Stdout, socket, os.Getenv("LANES_AGENT_ID"), os.Args[2], os.Args[3], false)
		case len(os.Args) == 5 && os.Args[2] == "--global":
			if socket == "" {
				socket = config.SocketPath()
			}
			hook.Client(os.Stdin, os.Stdout, socket, os.Getenv("LANES_AGENT_ID"), os.Args[3], os.Args[4], true)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lanes:", err)
		os.Exit(1)
	}
}

func run() error {
	assignee := flag.String("assignee", "", `whose tickets to show: "me", a user id, name, or email`)
	noTmux := flag.Bool("no-tmux", false, "run outside tmux as a read-only board")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  lanes [--assignee who] [--no-tmux]     the board (starts or attaches to tmux session "lanes")
  lanes auth logout                      forget the Linear sign-in
  lanes install-hooks [--yes] [--settings PATH]
                                         report every Claude session to lanes (edits ~/.claude/settings.json after asking)
  lanes uninstall-hooks [--yes] [--settings PATH]
                                         remove exactly what install-hooks added

flags:
`)
		flag.PrintDefaults()
	}
	if len(os.Args) > 1 && (os.Args[1] == "install-hooks" || os.Args[1] == "uninstall-hooks") {
		return hooksCommand(os.Args[1] == "uninstall-hooks", os.Args[2:])
	}
	flag.Parse()

	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}
	tokens := linear.Store{File: filepath.Join(stateDir, "linear-token.json")}

	if args := flag.Args(); len(args) > 0 {
		if len(args) == 2 && args[0] == "auth" && args[1] == "logout" {
			if err := tokens.Delete(); err != nil {
				return err
			}
			fmt.Println("signed out of Linear")
			return nil
		}
		flag.Usage()
		return fmt.Errorf("unknown command %q", args)
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	if *assignee != "" {
		cfg.Assignee = *assignee
	}

	tm := tmux.Client{}
	panel := os.Getenv("TMUX_PANE")
	switch {
	case *noTmux:
		panel = "" // read-only board even inside tmux
	case panel == "":
		return execInTmux(tm)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	oauth, err := linear.NewOAuth(ctx, tokens)
	if err != nil {
		return err
	}
	tr, err := linear.Dial(ctx, linear.Endpoint, os.Getenv("LINEAR_API_KEY"), oauth)
	if err != nil {
		return err
	}
	defer tr.Close()
	// First fetch happens before the TUI so a browser login never fights the alt screen.
	issues, err := tr.Issues(ctx, cfg.Assignee)
	if err != nil {
		return err
	}
	stop()

	store := state.Store{Dir: stateDir}
	var adapters []agent.Adapter
	for _, a := range []agent.Adapter{claude.New(), codex.New()} {
		if _, err := exec.LookPath(a.Name()); err == nil { // only tools that are installed
			adapters = append(adapters, a)
		}
	}
	opt := ui.Options{
		Tracker: tr, Adapters: adapters, Store: store, Config: cfg,
		Fleet:    fleet.Fleet{Adapters: adapters, Store: store, Tmux: tm, NoPrune: panel == ""},
		Assignee: cfg.Assignee, Issues: issues,
		LinearPoll: cfg.LinearPoll.Duration, ExternalPoll: cfg.ExternalPoll.Duration,
	}
	if panel != "" {
		cleanup, err := takeOver(tm, panel)
		if err != nil {
			return err
		}
		defer cleanup()
		placeholder, err := tm.SplitRight(panel, 65, []string{"sh", "-c",
			`printf '\n  Select an agent in lanes and press enter to show it here.\n'; exec cat >/dev/null`})
		if err != nil {
			return err
		}
		defer tm.KillPane(placeholder)
		if err := tm.SetOpt(placeholder, tmux.OptPlaceholder, "1"); err != nil {
			return err
		}
		opt.Tmux, opt.Panel, opt.Placeholder = tm, panel, placeholder

		sock := filepath.Join(stateDir, "lanes.sock")
		if srv, err := hook.Listen(sock); err != nil {
			// Agents still launch and work; they just report no live status or approvals.
			opt.Notice = "live status and approvals are off: " + err.Error()
		} else {
			defer srv.Close() // waiting hooks hang up; each agent's own prompt takes over
			if bin, err := os.Executable(); err == nil {
				opt.Hooks, opt.HookBin, opt.Socket = srv.Events(), bin, sock
			}
		}
		opt.Notify = notifier(tm, panel, cfg.NotifyOS)
	}
	_, err = tea.NewProgram(ui.New(opt)).Run()
	return err
}

// takeOver marks this pane as the lanes panel after making sure no other panel is
// live, and repairs anything a crashed panel left behind.
func takeOver(tm tmux.Client, panel string) (func(), error) {
	panes, err := tm.Panes()
	if err != nil {
		return nil, err
	}
	for _, p := range panes {
		if p.ID != panel && livePanel(p) {
			return nil, fmt.Errorf("lanes is already running in tmux pane %s (session %s)", p.ID, p.Session)
		}
	}
	if err := fleet.Reconcile(tm); err != nil {
		fmt.Fprintln(os.Stderr, "lanes: recovering tmux panes:", err)
	}
	if err := tm.SetOpt(panel, tmux.OptPanel, strconv.Itoa(os.Getpid())); err != nil {
		return nil, err
	}
	return func() { tm.UnsetOpt(panel, tmux.OptPanel) }, nil
}

func hooksCommand(uninstall bool, args []string) error {
	fs := flag.NewFlagSet("hooks", flag.ExitOnError)
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = config.Expand("~/.claude")
	}
	path := fs.String("settings", filepath.Join(dir, "settings.json"), "Claude Code settings file")
	fs.Parse(args)
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if !uninstall && strings.Contains(bin, "go-build") {
		return errors.New("install-hooks needs a lanes binary that stays put (go install it); this one is a temporary `go run` build")
	}
	return install.Run(*path, bin, uninstall, *yes, os.Stdin, os.Stdout)
}

// notifier shows text as a tmux message on the panel's client and, if enabled, as a
// desktop notification. Arguments go to exec directly: no shell sees ticket text.
func notifier(tm tmux.Client, panel string, desktop bool) func(string) {
	return func(text string) {
		tm.DisplayMessage(panel, "lanes: "+text)
		if !desktop {
			return
		}
		switch {
		case runtime.GOOS == "darwin":
			exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 1 of argv) with title \"lanes\"", "-e", "end run", text).Run()
		default:
			exec.Command("notify-send", "lanes", text).Run()
		}
	}
}

// livePanel reports whether a pane is marked as a panel whose process still runs;
// a panel killed by a signal leaves its mark behind but not its process.
func livePanel(p tmux.Pane) bool {
	pid, err := strconv.Atoi(p.Panel)
	if err != nil || p.Dead {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// execInTmux replaces this process with tmux running lanes in the "lanes" session,
// or attaches to it when a panel is already running there.
func execInTmux(tm tmux.Client) error {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return errors.New("lanes needs tmux (install it, or run with --no-tmux for a read-only board)")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := append([]string{self}, os.Args[1:]...)
	argv := append([]string{"tmux", "new-session", "-s", tmuxSession, "--"}, cmd...)
	if tm.HasSession(tmuxSession) {
		live := false
		panes, _ := tm.Panes()
		for _, p := range panes {
			live = live || livePanel(p)
		}
		if !live {
			if out, err := exec.Command(bin, append([]string{"new-window", "-t", "=" + tmuxSession + ":", "--"}, cmd...)...).CombinedOutput(); err != nil {
				return fmt.Errorf("tmux new-window: %w: %s", err, out)
			}
		}
		argv = []string{"tmux", "attach-session", "-t", "=" + tmuxSession}
	}
	return syscall.Exec(bin, argv, os.Environ())
}
