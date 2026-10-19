package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/config"
)

func press(w *keysWizard, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		w.Update(msg)
	}
}

func newWizard() *keysWizard {
	w := &keysWizard{keys: map[string]string{}, was: map[string]string{}}
	for _, a := range config.KeyActions {
		w.keys[a.Name], w.was[a.Name] = a.Default, a.Default
	}
	return w
}

func TestKeysWizard(t *testing.T) {
	w := newWizard()
	press(w, "S") // start = S, on to new_ticket
	press(w, "j") // fixed: refused
	if w.at != 1 || w.problem == "" || w.keys["start"] != "S" {
		t.Fatalf("at %d problem %q %v", w.at, w.problem, w.keys)
	}
	press(w, "x") // stop's key: asks to swap
	if w.at != 1 || w.swap != "x" {
		t.Fatalf("at %d swap %q", w.at, w.swap)
	}
	press(w, "x") // again: new_ticket = x, stop = N
	if w.keys["new_ticket"] != "x" || w.keys["stop"] != "N" || w.at != 2 {
		t.Fatalf("%v at %d", w.keys, w.at)
	}
	press(w, "enter", "backspace")                                      // send kept; approve set to its default
	if w.keys["send"] != "s" || w.keys["approve"] != "a" || w.at != 4 { // backspace moves on too
		t.Fatalf("%v at %d", w.keys, w.at)
	}
	for w.at < len(config.KeyActions) {
		press(w, "enter")
	}
	if err := config.CheckKeys(w.keys); err != nil {
		t.Fatal(err)
	}
	press(w, "backspace", "enter") // all defaults, save
	if !w.save || w.keys["start"] != "n" || w.keys["stop"] != "x" {
		t.Fatalf("save %v %v", w.save, w.keys)
	}
}

func TestKeysWizardBackspaceCantDuplicate(t *testing.T) {
	w := newWizard()
	press(w, "x", "x") // start ⇄ stop: start = x, stop = n
	for config.KeyActions[w.at].Name != "stop" {
		press(w, "enter")
	}
	press(w, "backspace") // stop's default x is start's now: asks
	if w.keys["stop"] != "n" || w.swap != "x" {
		t.Fatalf("%v swap %q", w.keys, w.swap)
	}
	press(w, "backspace") // again: swap back
	if w.keys["stop"] != "x" || w.keys["start"] != "n" {
		t.Fatalf("%v", w.keys)
	}
	if err := config.CheckKeys(w.keys); err != nil {
		t.Fatal(err)
	}
}
