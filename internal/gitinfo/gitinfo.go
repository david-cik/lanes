// Package gitinfo reads a working tree's state without changing anything (no fetch).
package gitinfo

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Info struct {
	Branch     string // "" when detached
	Upstream   string
	UpAhead    int
	UpBehind   int
	Base       string // origin/<default>, "" if unknown
	BaseAhead  int
	BaseBehind int
	Dirty      int // changed or untracked files
	Subject    string
	When       time.Time
}

func Git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBranch returns the name of origin's default branch.
func DefaultBranch(ctx context.Context, repo string) (string, error) {
	if ref, err := Git(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "origin/"), nil
	}
	for _, b := range []string{"main", "master"} {
		if _, err := Git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("cannot tell the default branch of %s (set origin/HEAD with `git remote set-head origin -a`)", repo)
}

// Read gathers Info for dir. Parts that don't apply (no upstream, no origin) stay zero.
func Read(ctx context.Context, dir string) (Info, error) {
	var in Info
	if _, err := Git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return in, err
	}
	in.Branch, _ = Git(ctx, dir, "branch", "--show-current")
	if up, err := Git(ctx, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		in.Upstream = up
		in.UpBehind, in.UpAhead = counts(ctx, dir, "@{upstream}...HEAD")
	}
	if def, err := DefaultBranch(ctx, dir); err == nil {
		in.Base = "origin/" + def
		in.BaseBehind, in.BaseAhead = counts(ctx, dir, in.Base+"...HEAD")
	}
	if st, err := Git(ctx, dir, "status", "--porcelain"); err == nil && st != "" {
		in.Dirty = len(strings.Split(st, "\n"))
	}
	if log, err := Git(ctx, dir, "log", "-1", "--format=%ct%x00%s"); err == nil {
		if ts, subj, ok := strings.Cut(log, "\x00"); ok {
			sec, _ := strconv.ParseInt(ts, 10, 64)
			in.When, in.Subject = time.Unix(sec, 0), subj
		}
	}
	return in, nil
}

// counts returns (left-only, right-only) commit counts for a symmetric range.
func counts(ctx context.Context, dir, rng string) (int, int) {
	out, err := Git(ctx, dir, "rev-list", "--left-right", "--count", rng)
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0
	}
	l, _ := strconv.Atoi(f[0])
	r, _ := strconv.Atoi(f[1])
	return l, r
}
