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
	"strconv"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/agent/codex"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/fleet"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tmux"
	"github.com/david-cik/lanes/internal/tracker/linear"
	"github.com/david-cik/lanes/internal/ui"
)

const tmuxSession = "lanes"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lanes:", err)
		os.Exit(1)
	}
}

func run() error {
	assignee := flag.String("assignee", "", `whose tickets to show: "me", a user id, name, or email`)
	noTmux := flag.Bool("no-tmux", false, "run outside tmux as a read-only board")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: lanes [--assignee who] [--no-tmux]\n       lanes auth logout\n")
		flag.PrintDefaults()
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
