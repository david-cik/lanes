package main

import (
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/config"
)

// keysCommand is `lanes keys`: a question per board action, answered by pressing the
// key, saved to the [keys] table. --reset puts every key back to its default.
func keysCommand(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	reset := fs.Bool("reset", false, "put every key back to its default")
	fs.Parse(args)
	path := config.Path()
	if *reset {
		if err := config.WriteKeys(path, nil); err != nil {
			return err
		}
		fmt.Println("keys are back to their defaults (" + path + ")")
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil { // the wizard only needs the keys; if they're what's wrong, start over
		fmt.Println("lanes: " + err.Error())
		if config.CheckKeys(cfg.Keys) != nil {
			fmt.Println("starting from the default keys")
			cfg.Keys = nil
		}
	}
	w := &keysWizard{keys: map[string]string{}}
	for _, a := range config.KeyActions {
		w.keys[a.Name] = cfg.Key(a.Name)
	}
	w.was = maps.Clone(w.keys)
	if _, err := tea.NewProgram(w).Run(); err != nil {
		return err
	}
	if !w.save {
		fmt.Println("nothing saved")
		return nil
	}
	if err := config.WriteKeys(path, w.keys); err != nil {
		return err
	}
	fmt.Println("saved to " + path + "; restart lanes (q, then lanes) to use them")
	return nil
}

type keysWizard struct {
	keys, was map[string]string // action → key: being chosen, and as they were
	at        int               // the question on screen; len(KeyActions) = the summary
	problem   string
	swap      string // a key another action has, pressed once: pressed again, they swap
	save      bool
}

func (w *keysWizard) Init() tea.Cmd { return nil }

func (w *keysWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return w, nil
	}
	key := k.String()
	w.problem = ""
	swap := w.swap
	w.swap = ""
	if key == "esc" || key == "ctrl+c" {
		return w, tea.Quit
	}
	if w.at == len(config.KeyActions) { // the summary
		switch key {
		case "enter", "y":
			if err := config.CheckKeys(w.keys); err != nil { // a backstop: never save a config lanes won't load
				w.problem = err.Error()
				return w, nil
			}
			w.save = true
			return w, tea.Quit
		case "left", "shift+tab":
			w.at--
		case "backspace":
			for _, a := range config.KeyActions {
				w.keys[a.Name] = a.Default
			}
		}
		return w, nil
	}
	a := config.KeyActions[w.at]
	switch key {
	case "enter":
		w.at++
	case "left", "shift+tab":
		w.at = max(w.at-1, 0)
	case "backspace":
		w.assign(a, a.Default, swap)
	default:
		if key == "space" || utf8.RuneCountInString(key) != 1 {
			w.problem = "use a single character (letter, digit, or symbol)"
			break
		}
		if slices.Contains(config.FixedKeys, key) {
			w.problem = key + " moves around the board; pick another"
			break
		}
		w.assign(a, key, swap)
	}
	return w, nil
}

// assign gives a key to action a, and moves on. A key another action has is only
// asked about the first time; the second (swap == key), the two actions swap keys.
func (w *keysWizard) assign(a config.KeyAction, key, swap string) {
	for _, o := range config.KeyActions {
		if o.Name == a.Name || w.keys[o.Name] != key {
			continue
		}
		if swap != key {
			w.problem = fmt.Sprintf("%s is already the key to %s · press it again to swap them (%s gets %s), or pick another",
				key, what(o), what(o), w.keys[a.Name])
			w.swap = key
			return
		}
		w.keys[o.Name] = w.keys[a.Name]
	}
	w.keys[a.Name] = key
	w.at++
}

// what is an action's question as a phrase: "Key to quit lanes?" → "quit lanes".
func what(a config.KeyAction) string {
	return strings.TrimSuffix(strings.TrimPrefix(a.Question, "Key to "), "?")
}

func (w *keysWizard) View() tea.View {
	var b strings.Builder
	n := len(config.KeyActions)
	if w.at < n {
		a := config.KeyActions[w.at]
		fmt.Fprintf(&b, "lanes keys · %d of %d\n\n%s\n\n", w.at+1, n, a.Question)
		fmt.Fprintf(&b, "  now:      %s\n", w.keys[a.Name])
		if w.keys[a.Name] != a.Default {
			fmt.Fprintf(&b, "  default:  %s\n", a.Default)
		}
		b.WriteString("\npress the key you want · enter keeps it · backspace = its default · ← back · esc quits without saving\n")
	} else {
		b.WriteString("lanes keys · done\n\n")
		changed := 0
		for _, a := range config.KeyActions {
			mark := "  "
			if w.keys[a.Name] != w.was[a.Name] {
				mark, changed = "* ", changed+1
			}
			note := ""
			if w.keys[a.Name] != a.Default {
				note = "   (default " + a.Default + ")"
			}
			fmt.Fprintf(&b, "%s%-3s %s%s\n", mark, w.keys[a.Name], what(a), note)
		}
		fmt.Fprintf(&b, "\n%d changed (*) · enter saves · backspace = all defaults · ← back · esc quits without saving\n", changed)
	}
	if w.problem != "" {
		b.WriteString("\n" + w.problem + "\n")
	}
	return tea.NewView(b.String())
}
