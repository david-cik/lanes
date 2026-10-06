package agent

import (
	"context"
	"os/exec"
	"testing"
)

func TestFillBranches(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "abc-12-thing"}, {"commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	agents := []Agent{{Cwd: repo}, {Cwd: t.TempDir()}}
	FillBranches(context.Background(), agents)
	if agents[0].Branch != "abc-12-thing" || agents[1].Branch != "" {
		t.Fatalf("got %+v", agents)
	}
}
