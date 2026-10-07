// Package tmux is a thin wrapper over the tmux CLI. Panes are addressed by ID (%N),
// which stays stable when a pane is swapped between sessions; session names do not.
package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Pane options lanes sets; user options travel with the pane across swaps.
const (
	OptAgent       = "@lanes_agent"
	OptPlaceholder = "@lanes_placeholder"
	OptPanel       = "@lanes_panel"
)

// Client runs tmux against the default server, or a named one (-L) when Socket is set.
type Client struct{ Socket string }

type Pane struct {
	ID          string
	Session     string
	Agent       string // value of @lanes_agent
	Placeholder bool
	Panel       string // value of @lanes_panel: the panel process's PID
	Dead        bool
}

func (c Client) run(args ...string) (string, error) {
	if c.Socket != "" {
		args = append([]string{"-L", c.Socket}, args...)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("tmux", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// NewSession starts a detached session running argv in cwd and returns its pane ID.
func (c Client) NewSession(name, cwd string, env, argv []string) (string, error) {
	args := []string{"new-session", "-d", "-s", name, "-c", cwd, "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	return c.run(append(append(args, "--"), argv...)...)
}

// SplitRight splits target horizontally without focusing the new pane.
func (c Client) SplitRight(target string, percent int, argv []string) (string, error) {
	args := []string{"split-window", "-h", "-d", "-l", fmt.Sprintf("%d%%", percent), "-t", target, "-P", "-F", "#{pane_id}", "--"}
	return c.run(append(args, argv...)...)
}

func (c Client) SetOpt(pane, key, val string) error {
	_, err := c.run("set-option", "-p", "-t", pane, key, val)
	return err
}

// Panes lists every pane on the server.
func (c Client) Panes() ([]Pane, error) {
	out, err := c.run("list-panes", "-a", "-F",
		"#{pane_id}\t#{session_name}\t#{"+OptAgent+"}\t#{"+OptPlaceholder+"}\t#{"+OptPanel+"}\t#{pane_dead}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			continue
		}
		panes = append(panes, Pane{ID: f[0], Session: f[1], Agent: f[2], Placeholder: f[3] == "1", Panel: f[4], Dead: f[5] == "1"})
	}
	return panes, nil
}

// Swap exchanges two panes' positions without changing focus.
func (c Client) Swap(a, b string) error {
	_, err := c.run("swap-pane", "-d", "-s", a, "-t", b)
	return err
}

// Join moves src next to dst (to its right) without focusing it.
func (c Client) Join(src, dst string, percent int) error {
	_, err := c.run("join-pane", "-h", "-d", "-l", fmt.Sprintf("%d%%", percent), "-s", src, "-t", dst)
	return err
}

// UnsetOpt removes a pane option.
func (c Client) UnsetOpt(pane, key string) error {
	_, err := c.run("set-option", "-p", "-u", "-t", pane, key)
	return err
}

func (c Client) Select(pane string) error {
	_, err := c.run("select-pane", "-t", pane)
	return err
}

// SendText types text literally, then presses Enter.
func (c Client) SendText(pane, text string) error {
	if _, err := c.run("send-keys", "-t", pane, "-l", "--", text); err != nil {
		return err
	}
	return c.SendKeys(pane, "Enter")
}

// SendKeys sends tmux key names (e.g. "C-c", "Enter").
func (c Client) SendKeys(pane string, keys ...string) error {
	_, err := c.run(append([]string{"send-keys", "-t", pane}, keys...)...)
	return err
}

func (c Client) KillSession(name string) error {
	_, err := c.run("kill-session", "-t", "="+name)
	return err
}

func (c Client) KillPane(pane string) error {
	_, err := c.run("kill-pane", "-t", pane)
	return err
}

func (c Client) HasSession(name string) bool {
	_, err := c.run("has-session", "-t", "="+name)
	return err == nil
}

// DisplayMessage shows text in the status line of the client viewing target for 4s.
func (c Client) DisplayMessage(target, text string) error {
	_, err := c.run("display-message", "-l", "-d", "4000", "-t", target, "--", text) // -l: no format expansion of ticket text
	return err
}

// SessionOf returns the name of the session a pane is in.
func (c Client) SessionOf(pane string) (string, error) {
	return c.run("display-message", "-p", "-t", pane, "#{session_name}")
}

// SessionOpt returns an option of the session that pane is in, and whether it is set
// on that session (rather than inherited from the global value). Targeting a pane id
// avoids session-name matching surprises.
func (c Client) SessionOpt(pane, key string) (string, bool) {
	if out, err := c.run("show-options", "-t", pane, key); err != nil || out == "" {
		return "", false
	}
	v, _ := c.run("show-options", "-v", "-t", pane, key) // -v: raw, unquoted value
	return v, true
}

func (c Client) SetSessionOpt(pane, key, val string) error {
	_, err := c.run("set-option", "-t", pane, key, val)
	return err
}

func (c Client) UnsetSessionOpt(pane, key string) error {
	_, err := c.run("set-option", "-u", "-t", pane, key)
	return err
}

// WindowOpt returns a window option's value and whether it is set on pane's window
// (rather than inherited from the global value).
func (c Client) WindowOpt(pane, key string) (string, bool) {
	if out, err := c.run("show-options", "-w", "-t", pane, key); err != nil || out == "" {
		return "", false
	}
	v, _ := c.run("show-options", "-wv", "-t", pane, key)
	return v, true
}

func (c Client) SetWindowOpt(pane, key, val string) error {
	_, err := c.run("set-option", "-w", "-t", pane, key, val)
	return err
}

func (c Client) UnsetWindowOpt(pane, key string) error {
	_, err := c.run("set-option", "-wu", "-t", pane, key)
	return err
}

// GlobalOpt returns a global (server-wide) option's value, e.g. "prefix".
func (c Client) GlobalOpt(key string) string {
	v, _ := c.run("show-options", "-gv", key)
	return v
}

// RootBinding returns the root-table (no prefix) binding line for key, or "".
func (c Client) RootBinding(key string) string { return c.binding("root", key) }

// PrefixBinding returns the prefix-table binding line for key, or "".
func (c Client) PrefixBinding(key string) string { return c.binding("prefix", key) }

// (list-keys with a key argument doesn't match punctuation keys like C-], so scan.)
func (c Client) binding(table, key string) string {
	out, _ := c.run("list-keys", "-T", table)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line) // flags such as -r may come before -T
		if i := slices.Index(f, "-T"); i >= 0 && i+2 < len(f) && f[i+1] == table && f[i+2] == key {
			return line
		}
	}
	return ""
}

// BoundCommand is the command part of a binding line from list-keys ("" if none).
func BoundCommand(line string) string {
	f := strings.Fields(line)
	if i := slices.Index(f, "-T"); i >= 0 && i+3 <= len(f) {
		return strings.Join(f[i+3:], " ")
	}
	return ""
}

// BindFocusToggle makes key, pressed anywhere in session, jump between the panel and
// the pane to its right. Outside session the key is passed through to the program.
func (c Client) BindFocusToggle(key, session, panel string) error {
	_, err := c.run("bind-key", "-n", key, "if-shell", "-F", "#{==:#{session_name},"+session+"}", Toggle(panel), "send-keys "+key)
	return err
}

// Toggle is the tmux command that jumps between the panel and the pane to its right.
func Toggle(panel string) string {
	return fmt.Sprintf("if-shell -F '#{==:#{pane_id},%s}' 'select-pane -R' 'select-pane -t %s'", panel, panel)
}

// BindPrefix makes prefix+key run action (a tmux command) inside session; elsewhere
// it runs fallback ("" = nothing).
func (c Client) BindPrefix(key, session, action, fallback string) error {
	args := []string{"bind-key", "-T", "prefix", key, "if-shell", "-F", "#{==:#{session_name}," + session + "}", action}
	if fallback != "" {
		args = append(args, fallback)
	}
	_, err := c.run(args...)
	return err
}

// RestorePrefix binds prefix+key back to cmd (a single tmux command), or unbinds it.
func (c Client) RestorePrefix(key, cmd string) error {
	if cmd == "" {
		_, err := c.run("unbind-key", "-T", "prefix", key)
		return err
	}
	_, err := c.run(append([]string{"bind-key", "-T", "prefix", key}, strings.Fields(cmd)...)...)
	return err
}

func (c Client) Unbind(key string) error {
	_, err := c.run("unbind-key", "-n", key)
	return err
}

// Capture returns the visible text of a pane.
func (c Client) Capture(pane string) (string, error) {
	return c.run("capture-pane", "-p", "-t", pane)
}
