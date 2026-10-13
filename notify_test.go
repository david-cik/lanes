package main

import "testing"

func TestNotifyProtocol(t *testing.T) {
	for _, c := range []struct{ via, term, want string }{
		{"auto", "iTerm2 3.7.1", "osc9"},
		{"", "WezTerm 20240203", "osc9"},
		{"auto", "ghostty 1.1.0", "osc9"},
		{"auto", "kitty(0.36.4)", "osc99"},
		{"auto", "foot(1.18.1)", "osc777"},
		{"auto", "XTerm(390)", ""},         // unknown: the system's notifier
		{"terminal", "XTerm(390)", "osc9"}, // forced
		{"system", "iTerm2 3.7.1", ""},
	} {
		if got := notifyProtocol(c.via, c.term); got != c.want {
			t.Errorf("%s / %q: got %q, want %q", c.via, c.term, got, c.want)
		}
	}
}
