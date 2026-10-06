// Package gh reads pull request state through the GitHub CLI (the user's own login).
package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Checks struct{ Pass, Fail, Pending, Skipped int }

type PR struct {
	Number int
	URL    string
	State  string // OPEN, MERGED, CLOSED
	Draft  bool
	Review string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	Checks Checks
	Failed []string // names of failing checks, at most 3
}

// Runner runs gh in dir and returns its stdout, or an error carrying stderr.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

func Exec(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// Unavailable explains why PR state can't be shown (gh missing or not signed in).
type Unavailable struct{ Reason string }

func (u Unavailable) Error() string { return u.Reason }

// Find returns the PR for branch in the repo at dir, or nil if there is none.
func Find(ctx context.Context, run Runner, dir, branch string) (*PR, error) {
	if branch == "" {
		return nil, nil
	}
	out, err := run(ctx, dir, "pr", "view", branch, "--json", "number,url,state,isDraft,reviewDecision,statusCheckRollup")
	if err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return nil, Unavailable{"gh is not installed"}
		case strings.Contains(msg, "no pull requests found"):
			return nil, nil
		case strings.Contains(msg, "gh auth login"), strings.Contains(msg, "not logged"):
			return nil, Unavailable{"gh is not signed in (run gh auth login)"}
		}
		return nil, fmt.Errorf("gh pr view: %s", firstLine(msg))
	}
	var r struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Draft  bool   `json:"isDraft"`
		Review string `json:"reviewDecision"`
		Checks []struct {
			Name       string `json:"name"`
			Context    string `json:"context"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"` // StatusContext
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}
	pr := &PR{Number: r.Number, URL: r.URL, State: r.State, Draft: r.Draft, Review: r.Review}
	for _, c := range r.Checks {
		result := c.Conclusion
		if c.State != "" { // StatusContext
			result = c.State
		} else if c.Status != "" && c.Status != "COMPLETED" {
			result = "PENDING"
		}
		switch result {
		case "SUCCESS", "NEUTRAL":
			pr.Checks.Pass++
		case "SKIPPED":
			pr.Checks.Skipped++
		case "PENDING", "EXPECTED", "":
			pr.Checks.Pending++
		default: // FAILURE, ERROR, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE
			pr.Checks.Fail++
			name := c.Name
			if name == "" {
				name = c.Context
			}
			if len(pr.Failed) < 3 {
				pr.Failed = append(pr.Failed, name)
			}
		}
	}
	return pr, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
