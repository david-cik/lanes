// Command lanes is a terminal board of tracker tickets and the local AI agents working them.
package main

import (
	"context"
	"encoding/json"
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
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/agent/codex"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/fleet"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/install"
	"github.com/david-cik/lanes/internal/sessions"
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
  lanes sessions [--days N] [--json] [words…]
                                         list running and recent Claude sessions (title, folder, ticket, recent prompts)
  lanes link <session-id> <TICKET>       put a session on a ticket
  lanes unlink <session-id>              take a session off its ticket

flags:
`)
		flag.PrintDefaults()
	}
	if len(os.Args) > 1 && (os.Args[1] == "install-hooks" || os.Args[1] == "uninstall-hooks") {
		return hooksCommand(os.Args[1] == "uninstall-hooks", os.Args[2:])
	}
	if len(os.Args) > 1 && (os.Args[1] == "sessions" || os.Args[1] == "link" || os.Args[1] == "unlink") {
		return sessionsCommand(os.Args[1], os.Args[2:])
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
		label, notice, restore := setupFocus(tm, panel, cfg.FocusKey)
		defer restore()
		opt.FocusLabel = label
		if notice != "" {
			opt.Notice = notice
		}

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

// sessionsCommand is `lanes sessions`, `lanes link`, `lanes unlink`: what a Claude
// session (or a person) uses to find a session and attach it to a ticket. The panel
// picks link changes up on its next refresh.
func sessionsCommand(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	days := fs.Int("days", 7, "include sessions active in the last N days")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}
	store := state.Store{Dir: stateDir}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch cmd {
	case "sessions":
		running, _ := claude.New().List(ctx)
		found := sessions.Match(sessions.List(running, *days, store), strings.Join(fs.Args(), " "))
		if *asJSON {
			if found == nil {
				found = []sessions.Session{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(found)
		}
		for _, s := range found {
			state := "ended"
			if s.Running {
				state = "running"
			}
			ticket := s.Ticket
			switch ticket {
			case "":
				ticket = "no ticket"
			case "-":
				ticket = "kept off tickets"
			}
			fmt.Printf("%s  %-7s  %s  %s  [%s]\n", short(s.ID), state, s.When.Format("Jan 02 15:04"), s.Title, ticket)
			fmt.Printf("          %s", s.Cwd)
			if len(s.Mentions) > 0 {
				fmt.Printf(" · mentions %s", strings.Join(s.Mentions, ", "))
			}
			fmt.Println()
			if s.First != "" {
				fmt.Printf("          first prompt: %s\n", s.First)
			}
		}
		return nil
	case "link", "unlink":
		want := 2
		if cmd == "unlink" {
			want = 1
		}
		if fs.NArg() != want {
			return fmt.Errorf("usage: lanes link <session-id> <TICKET> | lanes unlink <session-id>")
		}
		s, err := sessions.Lookup(fs.Arg(0), store)
		if err != nil {
			return err
		}
		ticket := state.NoTicket
		if cmd == "link" {
			ticket = fs.Arg(1)
		}
		if err := sessions.Link(ctx, store, s.ID, ticket); err != nil {
			return err
		}
		if cmd == "link" {
			fmt.Printf("linked %s (%s) to %s\n", short(s.ID), s.Title, strings.ToUpper(ticket))
		} else {
			fmt.Printf("took %s (%s) off its ticket\n", short(s.ID), s.Title)
		}
	}
	return nil
}

func short(id string) string { return id[:min(8, len(id))] }

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

// setupFocus makes moving between the board and the agent pane easy in the session the
// panel runs in: mouse support on, and one key (focus_key) that jumps between them,
// with a reminder in the status bar. Everything is restored when lanes quits; the key
// only acts in this session and passes through everywhere else.
func setupFocus(tm tmux.Client, panel, key string) (label, notice string, restore func()) {
	var undo []func()
	restore = func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	setOpt := func(name, val string) {
		prev, set := tm.SessionOpt(panel, name)
		if tm.SetSessionOpt(panel, name, val) != nil {
			return
		}
		undo = append(undo, func() {
			if set {
				tm.SetSessionOpt(panel, name, prev)
			} else {
				tm.UnsetSessionOpt(panel, name)
			}
		})
	}
	setOpt("mouse", "on")
	if key == "" || key == "none" {
		// No extra binding: move with the tmux prefix and arrows (or click). Drop a
		// focus binding an older lanes or a crashed panel may have left behind.
		if b := tm.RootBinding("C-]"); strings.Contains(b, "#{==:#{session_name},") {
			tm.Unbind("C-]")
		}
		label = keyLabel(tm.GlobalOpt("prefix")) + " ←/→"
		setOpt("status-right", fmt.Sprintf(" %s or click: board ⇄ agent ", label))
		return label, "", restore
	}
	session, err := tm.SessionOf(panel)
	if err != nil {
		return "", "", restore
	}
	label = keyLabel(key)
	existing := tm.RootBinding(key)
	if existing != "" && !strings.Contains(existing, "#{==:#{session_name},") { // the user's own
		return "", fmt.Sprintf("%s is already bound in your tmux, so lanes left it alone (set focus_key in the lanes config)", label), restore
	}
	if err := tm.BindFocusToggle(key, session, panel); err != nil {
		return "", "focus key: " + err.Error(), restore
	}
	undo = append(undo, func() { tm.Unbind(key) })
	setOpt("status-right", fmt.Sprintf(" %s or click: board ⇄ agent ", label))
	return label + " ⇄", "", restore
}

// keyLabel turns a tmux key name into what people call it: C-] → Ctrl-], M-Left → Alt-Left.
func keyLabel(key string) string {
	switch {
	case strings.HasPrefix(key, "C-"):
		return "Ctrl-" + key[2:]
	case strings.HasPrefix(key, "M-"):
		return "Alt-" + key[2:]
	}
	return key
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
