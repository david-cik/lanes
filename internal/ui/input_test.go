package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// rowY is the screen line of the first board row containing text.
func rowY(t *testing.T, m *Model, text string) int {
	t.Helper()
	for i, l := range strings.Split(view(m), "\n") {
		if strings.Contains(l, text) {
			return i
		}
	}
	t.Fatalf("no line containing %q:\n%s", text, view(m))
	return 0
}

func click(m *Model, x, y int) { m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) }

func TestMovingShowsAgentWithoutFocus(t *testing.T) {
	m, ft := controlModel(t)
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("j")) // onto agent "one"
	if strings.Join(ft.log, "|") != "swap %1 %P" {
		t.Fatalf("tmux calls %v", ft.log)
	}
	m.Update(key("l"))
	if ft.log[len(ft.log)-1] != "select %1" {
		t.Fatalf("l didn't focus the agent: %v", ft.log)
	}
}

func TestClickSelectsThenOpens(t *testing.T) {
	m, ft := controlModel(t)
	y := rowY(t, m, "two")
	click(m, 5, y)
	if r, _ := m.selected(); r.Agent == nil || r.Agent.Name != "two" {
		t.Fatalf("selected %+v", r)
	}
	if strings.Contains(strings.Join(ft.log, "|"), "select") {
		t.Fatalf("first click focused: %v", ft.log)
	}
	click(m, 5, y)
	if ft.log[len(ft.log)-1] != "select %2" {
		t.Fatalf("second click didn't open: %v", ft.log)
	}
}

func TestWheelMovesAndFooterClickPresses(t *testing.T) {
	m, _ := controlModel(t)
	m.cursor = 0
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 2 {
		t.Fatalf("cursor %d", m.cursor)
	}
	footer := strings.Split(view(m), "\n")[m.height-1]
	x := strings.Index(footer, "? keys")
	if x < 0 {
		t.Fatalf("footer %q", footer)
	}
	click(m, len([]rune(footer[:x])), m.height-1)
	if m.modal == nil || m.modal.title != "Keys" {
		t.Fatalf("footer click didn't open help: %+v", m.modal)
	}
}

func TestClickModalChoiceAndConfirm(t *testing.T) {
	m, _ := controlModel(t)
	m.cursorTo(t, "outside")
	m.Update(key("t"))
	click(m, 4, rowY(t, m, "ABC-1"))
	if l, _ := m.opt.Store.Links(); l["claude:x9"] != "ABC-1" {
		t.Fatalf("links %v", l)
	}
	m.cursorTo(t, "one")
	m.Update(key("x"))
	click(m, 3, rowY(t, m, "  n    no"))
	if m.modal != nil {
		t.Fatal("clicking no didn't close the confirm")
	}
}

func TestFilter(t *testing.T) {
	m, _ := controlModel(t)
	for _, k := range []string{"/", "o", "u", "t"} {
		m.Update(key(k))
	}
	v := view(m)
	if !strings.Contains(v, "outside") || strings.Contains(v, "ABC-1 fix it") || !strings.Contains(v, "/out") {
		t.Fatalf("filtered view:\n%s", v)
	}
	m.Update(key("enter"))
	m.Update(key("j")) // navigates again, not typed
	if m.filter != "out" {
		t.Fatalf("filter %q", m.filter)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.filter != "" || !strings.Contains(view(m), "ABC-1 fix it") {
		t.Fatalf("esc didn't clear:\n%s", view(m))
	}
	m.Update(key("/"))
	for _, k := range []string{"f", "i", "x"} { // ticket title: its agents stay too
		m.Update(key(k))
	}
	if v := view(m); !strings.Contains(v, "one") || strings.Contains(v, "outside") {
		t.Fatalf("ticket filter:\n%s", v)
	}
}

func TestClickBelowShortListPicksNothing(t *testing.T) {
	m, _ := controlModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 6}) // room for two items
	picked := ""
	m.modal = &modal{title: "pick", items: []choice{{"first", "1"}, {"second", "2"}, {"third", "3"}},
		onChoose: func(c choice) tea.Cmd { picked = c.value; return nil }}
	click(m, 2, m.height-1) // the footer, just below the visible items
	if picked != "" {
		t.Fatalf("picked %q from off screen", picked)
	}
	click(m, 2, rowY(t, m, "first"))
	if picked != "1" {
		t.Fatalf("picked %q", picked)
	}
}
