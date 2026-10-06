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
