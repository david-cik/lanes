//go:build integration

package main

import (
	"fmt"
	"math/rand/v2"
	"os/exec"
	"testing"

	"github.com/david-cik/lanes/internal/tmux"
)

func TestGuardPrefixRestoresExactly(t *testing.T) {
	tm := tmux.Client{Socket: fmt.Sprintf("lanes-main-test-%d", rand.Int())}
	panel, err := tm.NewSession("lanes", t.TempDir(), nil, []string{"sh", "-c", "exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tm.KillSession("lanes") })
	for _, c := range []struct{ prefix, before string }{
		{"C-b", "send-prefix"}, // tmux's default prefix and its stock binding
		{"C-a", ""},            // a custom prefix nobody bound to send-prefix
	} {
		if err := exec.Command("tmux", "-L", tm.Socket, "set-option", "-g", "prefix", c.prefix).Run(); err != nil {
			t.Fatal(err)
		}
		var undo []func()
		if !guardPrefix(tm, "lanes", panel, &undo) {
			t.Fatalf("%s: not guarded", c.prefix)
		}
		for _, u := range undo {
			u()
		}
		if got := tmux.BoundCommand(tm.PrefixBinding(c.prefix)); got != c.before {
			t.Fatalf("%s restored to %q, was %q", c.prefix, got, c.before)
		}
	}
}
