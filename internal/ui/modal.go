package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type choice struct{ label, value string }

// action is a single-key choice in an action modal; run reports whether the modal
// stays open (because it replaced itself, e.g. with an input).
type action struct {
	key, label string
	run        func() (cmd tea.Cmd, keepOpen bool)
}

// modal is the one overlay at a time: a filterable list, a text input, or a yes/no.
type modal struct {
	id       string // identifies modals that update later (e.g. "assignee")
	title    string
	lines    []string // extra text shown above the list / question
	items    []choice
	filter   string
	pick     int
	input    bool
	text     string
	confirm  bool
	actions  []action
	hint     string // footer text; "" = the default for the modal's kind
	onChoose func(choice) tea.Cmd
	onInput  func(string) tea.Cmd
	onYes    func() tea.Cmd
}

func (md *modal) visible() []choice {
	f := strings.ToLower(md.filter)
	var out []choice
	for _, c := range md.items {
		if f == "" || strings.Contains(strings.ToLower(c.label), f) {
			out = append(out, c)
		}
	}
	return out
}

// key handles a keypress; done reports that the modal should close.
func (md *modal) key(k tea.KeyPressMsg) (cmd tea.Cmd, done bool) {
	s := k.String()
	if s == "esc" {
		return nil, true
	}
	switch {
	case md.actions != nil:
		for _, a := range md.actions {
			if a.key == s {
				cmd, keep := a.run()
				return cmd, !keep
			}
		}
	case md.confirm:
		switch s {
		case "y", "enter":
			return md.onYes(), true
		case "n":
			return nil, true
		}
	case md.input:
		switch s {
		case "enter":
			return md.onInput(md.text), true
		case "backspace":
			md.text = dropLast(md.text)
		default:
			md.text += k.Text
		}
	default:
		vis := md.visible()
		switch s {
		case "down", "ctrl+n":
			md.pick = min(md.pick+1, max(len(vis)-1, 0))
		case "up", "ctrl+p":
			md.pick = max(md.pick-1, 0)
		case "enter":
			if len(vis) == 0 {
				return nil, false
			}
			return md.onChoose(vis[md.pick]), true
		case "backspace":
			md.filter, md.pick = dropLast(md.filter), 0
		default:
			if k.Text != "" {
				md.filter, md.pick = md.filter+k.Text, 0
			}
		}
	}
	return nil, false
}

func dropLast(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func (md *modal) help() string {
	switch {
	case md.hint != "":
		return md.hint
	case md.actions != nil:
		return "esc close"
	case md.confirm:
		return "y/enter confirm · n/esc cancel"
	case md.input:
		return "enter send · esc cancel"
	}
	return "type to filter · ↑/↓ · enter select · esc cancel"
}

// head is the modal's title and text, wrapped to width: what view draws above the
// choices, and what keyAt counts to find them.
func (md *modal) head(width int) []string {
	out := wrap(accentS.Render(md.title), width)
	for _, l := range md.lines {
		out = append(out, wrap(l, width)...)
	}
	return out
}

// wrap breaks text (styles allowed) into lines of at most width columns; lines after
// the first keep the text's own indent.
func wrap(s string, width int) []string {
	plain := ansi.Strip(s)
	ind := plain[:len(plain)-len(strings.TrimLeft(plain, " "))]
	if width <= len(ind)+10 {
		return []string{s}
	}
	out := strings.Split(ansi.Wrap(s, width, ""), "\n")
	if ind == "" || len(out) == 1 {
		return out
	}
	rest := strings.Split(ansi.Wrap(strings.Join(out[1:], " "), width-len(ind), ""), "\n")
	for i := range rest {
		rest[i] = ind + strings.TrimLeft(rest[i], " ")
	}
	return append(out[:1], rest...)
}

func (md *modal) view(b *strings.Builder, width, body int) {
	head := md.head(width)
	for _, l := range head {
		b.WriteString(l + "\n")
	}
	used := len(head)
	switch {
	case md.actions != nil:
		for _, a := range md.actions {
			b.WriteString(trunc(fmt.Sprintf("  %-4s %s", a.key, a.label), width) + "\n")
			used++
		}
	case md.confirm:
		b.WriteString(fmt.Sprintf("  %-4s %s\n  %-4s %s\n", "y", "yes", "n", "no"))
		used += 2
	case md.input:
		in := wrap("> "+md.text+"▏", width)
		b.WriteString(strings.Join(in, "\n") + "\n")
		used += len(in)
	default:
		b.WriteString("filter: " + md.filter + "▏\n")
		used++
		vis := md.visible()
		room := max(body-used, 1)
		start := max(md.pick-room+1, 0)
		end := min(start+room, len(vis))
		for i := start; i < end; i++ {
			line := trunc("  "+vis[i].label, width)
			if i == md.pick {
				line = sel.Render(line)
			}
			b.WriteString(line + "\n")
			used++
		}
	}
	b.WriteString(strings.Repeat("\n", max(body-used, 0)))
}
