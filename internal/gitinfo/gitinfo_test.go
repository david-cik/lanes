package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func TestRead(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "o.git")
	repo := filepath.Join(t.TempDir(), "r")
	sh(t, "/", "git", "init", "-q", "--bare", "-b", "main", bare)
	sh(t, "/", "git", "clone", "-q", bare, repo)
	sh(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(t, repo, "git", "push", "-q", "-u", "origin", "main")
	sh(t, repo, "git", "remote", "set-head", "origin", "-a")
	sh(t, repo, "git", "switch", "-q", "-c", "abc-1/x")
	sh(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "one")
	sh(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "fix: two")
	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644)

	in, err := Read(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if in.Branch != "abc-1/x" || in.Upstream != "" || in.Base != "origin/main" || in.BaseAhead != 2 || in.BaseBehind != 0 || in.Dirty != 1 || in.Subject != "fix: two" || in.When.IsZero() {
		t.Fatalf("%+v", in)
	}

	sh(t, repo, "git", "push", "-q", "-u", "origin", "abc-1/x")
	sh(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "three")
	in, _ = Read(ctx, repo)
	if in.Upstream != "origin/abc-1/x" || in.UpAhead != 1 || in.UpBehind != 0 {
		t.Fatalf("%+v", in)
	}

	sh(t, repo, "git", "switch", "-q", "--detach", "HEAD")
	if in, _ = Read(ctx, repo); in.Branch != "" {
		t.Fatalf("detached: %+v", in)
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := Read(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("err %v", err)
	}
}
