// Command lanes is a terminal board of tracker tickets and the local AI agents working them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/tracker/linear"
	"github.com/david-cik/lanes/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lanes:", err)
		os.Exit(1)
	}
}

func run() error {
	assignee := flag.String("assignee", "", `whose tickets to show: "me", a user id, name, or email`)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: lanes [--assignee who]\n       lanes auth logout\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}
	store := linear.Store{File: filepath.Join(stateDir, "linear-token.json")}

	if args := flag.Args(); len(args) > 0 {
		if len(args) == 2 && args[0] == "auth" && args[1] == "logout" {
			if err := store.Delete(); err != nil {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	key := os.Getenv("LINEAR_API_KEY")
	oauth, err := linear.NewOAuth(ctx, store)
	if err != nil {
		return err
	}
	tr, err := linear.Dial(ctx, linear.Endpoint, key, oauth)
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

	m := ui.New(ui.Options{
		Tracker:      tr,
		Adapters:     []agent.Adapter{claude.New()},
		Assignee:     cfg.Assignee,
		Issues:       issues,
		LinearPoll:   cfg.LinearPoll.Duration,
		ExternalPoll: cfg.ExternalPoll.Duration,
	})
	_, err = tea.NewProgram(m).Run()
	return err
}
