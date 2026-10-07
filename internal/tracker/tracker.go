// Package tracker defines the issue-tracker view lanes needs.
package tracker

import (
	"context"
	"time"
)

type Issue struct {
	Key        string // human identifier, e.g. ABC-12
	Title      string
	URL        string
	Team       string // team display name
	State      string // the team's own workflow state name
	StateType  string // triage|backlog|unstarted|started|completed|canceled|duplicate
	BranchName string // tracker-suggested git branch
	UpdatedAt  time.Time
	Pool       bool // unassigned and not started: offered to pick up, not assigned to anyone
}

type User struct {
	ID    string
	Name  string
	Email string
}

// IssueDetail adds what launching an agent needs.
type IssueDetail struct {
	Issue
	Description string
	PRURLs      []string // linked GitHub pull requests
}

// Tracker lists open issues for an assignee and the users that can be picked.
type Tracker interface {
	// Issues returns open (not completed/canceled/duplicate) issues; assignee may be "me".
	Issues(ctx context.Context, assignee string) ([]Issue, error)
	Issue(ctx context.Context, key string) (IssueDetail, error)
	Users(ctx context.Context) ([]User, error)
}

// Pooler lists the unassigned, not-yet-started issues of the given teams.
type Pooler interface {
	Pool(ctx context.Context, teams []string) ([]Issue, error)
}

// Claimer assigns an issue to the signed-in user and moves it to the team's first
// started state (by order, the team's states in workflow order if known), returning it.
type Claimer interface {
	Claim(ctx context.Context, key, team string, order []string) (string, error)
}
