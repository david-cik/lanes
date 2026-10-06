//go:build integration

package fleet

import (
	"fmt"
	"math/rand/v2"
	"os/exec"
	"testing"

	"github.com/david-cik/lanes/internal/tmux"
)

func TestReconcileAfterPanelCrash(t *testing.T) {
	tm := tmux.Client{Socket: fmt.Sprintf("lanes-test-%d", rand.Int())}
	t.Cleanup(func() { exec.Command("tmux", "-L", tm.Socket, "kill-server").Run() })
	sleep := []string{"sh", "-c", "exec sleep 600"}
	must := func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	panel := must(tm.NewSession("lanes", "/", nil, sleep))
	slot := must(tm.SplitRight(panel, 65, sleep))
	tm.SetOpt(slot, tmux.OptPlaceholder, "1")
	a := must(tm.NewSession("lanes-a", "/", nil, sleep))
	tm.SetOpt(a, tmux.OptAgent, "a")
	b := must(tm.NewSession("lanes-b", "/", nil, sleep))
	tm.SetOpt(b, tmux.OptAgent, "b")
	if err := tm.Swap(a, slot); err != nil { // a is on screen
		t.Fatal(err)
	}
	if err := tm.KillPane(panel); err != nil { // the panel crashes
		t.Fatal(err)
	}

	if err := Reconcile(tm); err != nil {
		t.Fatal(err)
	}
	panes, err := tm.Panes()
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]string{}
	for _, p := range panes {
		if p.Placeholder {
			t.Fatalf("placeholder survived: %+v", p)
		}
		where[p.Agent] = p.Session
	}
	if where["a"] != "lanes-a" || where["b"] != "lanes-b" || len(panes) != 2 {
		t.Fatalf("panes after reconcile: %+v", panes)
	}
}
