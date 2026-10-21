package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// KeyAction is a board action whose key can be changed in the [keys] table.
type KeyAction struct {
	Name     string // its name in [keys]
	Default  string
	Question string // what `lanes keys` asks
}

var KeyActions = []KeyAction{
	{"start", "n", "Key to start an agent on a ticket?"},
	{"new_ticket", "N", "Key to create a new ticket (drafted by an agent)?"},
	{"send", "s", "Key to send text to an agent?"},
	{"approve", "a", "Key to answer an agent's waiting request?"},
	{"stop", "x", "Key to stop an agent?"},
	{"adopt", "A", "Key to adopt a session lanes didn't start?"},
	{"coordinator", "C", "Key to show or start a project's coordinator?"},
	{"review", "R", "Key to start a review agent on a pull request?"},
	{"info", "i", "Key to show a ticket's card?"},
	{"open", "o", "Key to open a ticket in the browser?"},
	{"prs", "p", "Key to list a ticket's pull requests?"},
	{"details", "d", "Key to show details under the board?"},
	{"link", "t", "Key to move a session to a ticket?"},
	{"suggest", "T", "Key to suggest tickets for unlinked sessions?"},
	{"ask", ":", "Key to ask an agent a question?"},
	{"filter", "/", "Key to filter the board?"},
	{"assignee", "u", "Key to show someone else's tickets?"},
	{"refresh", "r", "Key to refresh?"},
	{"help", "?", "Key to show all the keys?"},
	{"quit", "q", "Key to quit lanes?"},
}

// FixedKeys move around the board and can't be given to an action.
var FixedKeys = []string{"j", "k", "g", "G", "[", "]", "l", " "}

// Key is the key for a board action, from [keys] or its default.
func (c Config) Key(action string) string {
	if k := c.Keys[action]; k != "" {
		return k
	}
	for _, a := range KeyActions {
		if a.Name == action {
			return a.Default
		}
	}
	return ""
}

// CheckKeys reports what's wrong with a [keys] table: an unknown action, a key that
// isn't one character, a fixed key, or two actions on one key.
func CheckKeys(keys map[string]string) error {
	c := Config{Keys: keys}
	for name := range keys {
		if !slices.ContainsFunc(KeyActions, func(a KeyAction) bool { return a.Name == name }) {
			return fmt.Errorf("keys: unknown action %q", name)
		}
	}
	used := map[string]string{}
	for _, a := range KeyActions {
		k := c.Key(a.Name)
		switch {
		case utf8.RuneCountInString(k) != 1 || k < " " || k == "\x7f":
			return fmt.Errorf("keys.%s must be one character, got %q", a.Name, k)
		case slices.Contains(FixedKeys, k):
			return fmt.Errorf("keys.%s: %q moves around the board and can't be changed", a.Name, k)
		case used[k] != "":
			return fmt.Errorf("keys.%s and keys.%s are both %q", used[k], a.Name, k)
		}
		used[k] = a.Name
	}
	return nil
}

// WriteKeys replaces the [keys] table in the config file at path with keys (only the
// ones that differ from their defaults), leaving the rest of the file as it is. No keys
// removes the table.
func WriteKeys(path string, keys map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out []string
	in, top := false, true // in the [keys] table; before any table
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in, top = t == "[keys]" || strings.HasPrefix(t, "[keys]") && strings.HasPrefix(strings.TrimSpace(t[len("[keys]"):]), "#"), false
		}
		// keys written by hand as keys.start = … or keys = { … } at the top
		if top && (strings.HasPrefix(t, "keys.") || strings.HasPrefix(strings.ReplaceAll(t, " ", ""), "keys=")) {
			continue
		}
		if !in {
			out = append(out, l)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	var table []string
	for _, a := range KeyActions {
		if k := keys[a.Name]; k != "" && k != a.Default {
			table = append(table, fmt.Sprintf("%s = %q", a.Name, k))
		}
	}
	if len(table) > 0 {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(append(out, "[keys] # set by `lanes keys`; `lanes keys --reset` removes it"), table...)
	}
	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	if text == "" && b == nil { // no file and nothing to say: leave it that way
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write a copy and rename it over, so a crash can't leave half a config.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
