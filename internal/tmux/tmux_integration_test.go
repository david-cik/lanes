//go:build integration

package tmux

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T) Client {
	c := Client{Socket: fmt.Sprintf("lanes-test-%d", rand.Int())}
	t.Cleanup(func() { c.run("kill-server") })
	return c
}

func sleeper(msg string) []string { return []string{"sh", "-c", "echo " + msg + "; exec sleep 600"} }

func find(t *testing.T, c Client, id string) Pane {
	ps, err := c.Panes()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("pane %s not found in %+v", id, ps)
	return Pane{}
}

func TestSwapMovesPanesAndOptions(t *testing.T) {
	c := server(t)
	panel, err := c.NewSession("lanes", t.TempDir(), nil, sleeper("PANEL"))
	if err != nil {
		t.Fatal(err)
	}
	slot, err := c.SplitRight(panel, 65, sleeper("SLOT"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.NewSession("lanes-a", t.TempDir(), []string{"LANES_AGENT_ID=a"}, sleeper("A"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetOpt(a, OptAgent, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetOpt(slot, OptPlaceholder, "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Swap(a, slot); err != nil {
		t.Fatal(err)
	}
	if p := find(t, c, a); p.Session != "lanes" || p.Agent != "a" {
		t.Fatalf("agent pane after swap: %+v", p)
	}
	if p := find(t, c, slot); p.Session != "lanes-a" || !p.Placeholder {
		t.Fatalf("placeholder after swap: %+v", p)
	}
}

func TestSendTextIsLiteral(t *testing.T) {
	c := server(t)
	p, err := c.NewSession("s", t.TempDir(), nil, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendText(p, `echo "a b" 'c' $((1+2)) ;`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := c.Capture(p)
		if strings.Contains(out, "a b c 3") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, _ := c.Capture(p)
	t.Fatalf("output:\n%s", out)
}

func TestKillAndHasSession(t *testing.T) {
	c := server(t)
	if _, err := c.NewSession("gone", t.TempDir(), nil, sleeper("x")); err != nil {
		t.Fatal(err)
	}
	if !c.HasSession("gone") {
		t.Fatal("session missing")
	}
	if err := c.KillSession("gone"); err != nil {
		t.Fatal(err)
	}
	if c.HasSession("gone") {
		t.Fatal("session survived kill")
	}
}

func TestPanesWithNoServer(t *testing.T) {
	ps, err := Client{Socket: fmt.Sprintf("lanes-none-%d", rand.Int())}.Panes()
	if err != nil || ps != nil {
		t.Fatalf("got %+v err=%v", ps, err)
	}
}

func TestDisplayMessageIsLiteral(t *testing.T) {
	c := server(t)
	p, err := c.NewSession("s", t.TempDir(), nil, sleeper("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DisplayMessage(p, "fix #{pane_id} #[bold]"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOptionsAndFocusBinding(t *testing.T) {
	c := server(t)
	panel, err := c.NewSession("lanes", t.TempDir(), nil, sleeper("p"))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.SessionOf(panel); s != "lanes" {
		t.Fatalf("session %q", s)
	}
	if _, set := c.SessionOpt(panel, "mouse"); set {
		t.Fatal("mouse unexpectedly set on the session")
	}
	c.SetSessionOpt(panel, "mouse", "on")
	if v, set := c.SessionOpt(panel, "mouse"); !set || v != "on" {
		t.Fatalf("mouse %q %v", v, set)
	}
	c.UnsetSessionOpt(panel, "mouse")
	if _, set := c.SessionOpt(panel, "mouse"); set {
		t.Fatal("unset failed")
	}
	if c.RootBinding("C-]") != "" {
		t.Fatal("C-] bound by default")
	}
	if err := c.BindFocusToggle("C-]", "lanes", panel); err != nil {
		t.Fatal(err)
	}
	if b := c.RootBinding("C-]"); !strings.Contains(b, "#{==:#{session_name},lanes}") || !strings.Contains(b, "select-pane -t "+panel) {
		t.Fatalf("binding %q", b)
	}
	c.Unbind("C-]")
	if c.RootBinding("C-]") != "" {
		t.Fatal("unbind failed")
	}
}

func TestSessionOptRoundTripsQuotedValues(t *testing.T) {
	c := server(t)
	p, err := c.NewSession("s", t.TempDir(), nil, sleeper("x"))
	if err != nil {
		t.Fatal(err)
	}
	want := `#[fg=red]%H:%M "q" it's`
	c.SetSessionOpt(p, "status-right", want)
	got, set := c.SessionOpt(p, "status-right")
	if !set || got != want {
		t.Fatalf("got %q set=%v", got, set)
	}
	c.SetSessionOpt(p, "status-right", got) // restoring must not add quotes
	if again, _ := c.SessionOpt(p, "status-right"); again != want {
		t.Fatalf("after restore %q", again)
	}
}

func TestPrefixPaneBindingRestores(t *testing.T) {
	c := server(t)
	if _, err := c.NewSession("lanes", t.TempDir(), nil, sleeper("p")); err != nil {
		t.Fatal(err)
	}
	before := c.PrefixBinding("l")
	if err := c.BindPrefix("l", "lanes", "select-pane -R", "last-window"); err != nil {
		t.Fatal(err)
	}
	if b := c.PrefixBinding("l"); !strings.Contains(b, "#{==:#{session_name},lanes}") || !strings.Contains(b, "select-pane -R") || !strings.Contains(b, "last-window") {
		t.Fatalf("binding %q", b)
	}
	c.RestorePrefix("l", "last-window")
	if after := c.PrefixBinding("l"); strings.Join(strings.Fields(after), " ") != strings.Join(strings.Fields(before), " ") {
		t.Fatalf("restored %q, was %q", after, before)
	}
	c.BindPrefix("h", "lanes", "select-pane -L", "")
	c.RestorePrefix("h", "")
	if b := c.PrefixBinding("h"); b != "" {
		t.Fatalf("h still bound: %q", b)
	}
}

func TestPrefixBindingFoundWithRepeatFlag(t *testing.T) {
	c := server(t)
	if _, err := c.NewSession("lanes", t.TempDir(), nil, sleeper("p")); err != nil {
		t.Fatal(err)
	}
	c.run("bind-key", "-r", "-T", "prefix", "h", "select-pane", "-L")
	b := c.PrefixBinding("h")
	if !strings.Contains(b, " -r ") || BoundCommand(b) != "select-pane -L" {
		t.Fatalf("binding %q command %q", b, BoundCommand(b))
	}
}

func TestDoublePrefixToggleRestores(t *testing.T) {
	c := server(t)
	panel, err := c.NewSession("lanes", t.TempDir(), nil, sleeper("p"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BindPrefix("C-b", "lanes", Toggle(panel), "send-prefix"); err != nil {
		t.Fatal(err)
	}
	if b := c.PrefixBinding("C-b"); !strings.Contains(b, "select-pane -t "+panel) || !strings.Contains(b, "send-prefix") {
		t.Fatalf("binding %q", b)
	}
	c.RestorePrefix("C-b", "send-prefix")
	if BoundCommand(c.PrefixBinding("C-b")) != "send-prefix" {
		t.Fatalf("restored %q", c.PrefixBinding("C-b"))
	}
}
