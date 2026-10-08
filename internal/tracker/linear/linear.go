// Package linear implements tracker.Tracker over Linear's hosted MCP server.
package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-cik/lanes/internal/tracker"
)

const Endpoint = "https://mcp.linear.app/mcp"

var issueFields = []string{"id", "title", "url", "gitBranchName", "status", "statusType", "team", "updatedAt", "project"}

// Client talks to Linear's MCP server over one long-lived session. A session that
// times out or breaks is dropped, and the next call opens a new one.
type Client struct {
	mu   sync.Mutex
	s    *mcp.ClientSession
	dial func(context.Context) (*mcp.ClientSession, error)
}

var (
	_ tracker.Tracker = (*Client)(nil)
	_ tracker.Pooler  = (*Client)(nil)
	_ tracker.Claimer = (*Client)(nil)
)

// Dial connects to endpoint. With apiKey set it sends it as a Bearer token;
// otherwise oauth (may be nil) handles authorization.
func Dial(ctx context.Context, endpoint, apiKey string, oauth auth.OAuthHandler) (*Client, error) {
	return connect(ctx, func() mcp.Transport {
		t := &mcp.StreamableClientTransport{Endpoint: endpoint}
		if apiKey != "" {
			t.HTTPClient = &http.Client{Transport: bearer{apiKey, http.DefaultTransport}}
		} else {
			t.OAuthHandler = oauth
		}
		return t
	})
}

// Connect starts an MCP session over any transport (tests use in-memory ones, which
// can't be reopened: after a broken call it stays broken).
func Connect(ctx context.Context, t mcp.Transport) (*Client, error) {
	return connect(ctx, func() mcp.Transport { return t })
}

func connect(ctx context.Context, transport func() mcp.Transport) (*Client, error) {
	c := &Client{dial: func(ctx context.Context) (*mcp.ClientSession, error) {
		s, err := mcp.NewClient(&mcp.Implementation{Name: "lanes", Version: "dev"}, nil).Connect(ctx, transport(), nil)
		if err != nil {
			return nil, fmt.Errorf("linear: connect: %w", err)
		}
		return s, nil
	}}
	s, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.s = s
	return c, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s == nil {
		return nil
	}
	return c.s.Close()
}

// session returns the open session, reopening one that was dropped.
func (c *Client) session(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s == nil {
		s, err := c.dial(ctx)
		if err != nil {
			return nil, err
		}
		c.s = s
	}
	return c.s, nil
}

// drop closes a session that failed, unless another call already replaced it.
func (c *Client) drop(s *mcp.ClientSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s == s {
		c.s = nil
		go s.Close() // Close waits for calls still on it; don't hold the lock meanwhile
	}
}

func (c *Client) Issues(ctx context.Context, assignee string) ([]tracker.Issue, error) {
	return c.list(ctx, map[string]any{"assignee": assignee, "fields": issueFields, "limit": 250})
}

// Pool lists each team's unassigned issues in a not-started (Todo-type) state.
func (c *Client) Pool(ctx context.Context, teams []string) ([]tracker.Issue, error) {
	var all []tracker.Issue
	for _, t := range teams {
		// "null" is how Linear's list_issues asks for issues with no assignee.
		is, err := c.list(ctx, map[string]any{"team": t, "assignee": "null", "state": "unstarted", "fields": issueFields, "limit": 250})
		if err != nil {
			return nil, err
		}
		for i := range is {
			is[i].Pool = true
		}
		all = append(all, is...)
	}
	return all, nil
}

// Claim assigns key to the signed-in user and moves it to the team's first started
// state. Linear doesn't report its states' order, so that's the first started one in
// order (the configured state_order), else "In Progress", else any started state.
func (c *Client) Claim(ctx context.Context, key, team string, order []string) (string, error) {
	b, err := c.call(ctx, "list_issue_statuses", map[string]any{"team": team})
	if err != nil {
		return "", err
	}
	var states []struct{ Name, Type string }
	if err := json.Unmarshal(b, &states); err != nil {
		return "", fmt.Errorf("linear: list_issue_statuses: %w", err)
	}
	var started []string
	for _, s := range states {
		if s.Type == "started" {
			started = append(started, s.Name)
		}
	}
	if len(started) == 0 {
		return "", fmt.Errorf("linear: team %s has no started state", team)
	}
	state := started[0]
	if slices.Contains(started, "In Progress") {
		state = "In Progress"
	}
	for _, s := range order {
		if slices.Contains(started, s) {
			state = s
			break
		}
	}
	_, err = c.call(ctx, "save_issue", map[string]any{"id": key, "assignee": "me", "state": state})
	return state, err
}

func (c *Client) list(ctx context.Context, args map[string]any) ([]tracker.Issue, error) {
	var all []tracker.Issue
	seen := map[string]bool{}
	for {
		b, err := c.call(ctx, "list_issues", args)
		if err != nil {
			return nil, err
		}
		issues, next, err := parseIssues(b)
		if err != nil {
			return nil, fmt.Errorf("linear: list_issues: %w", err)
		}
		all = append(all, issues...)
		if next == "" {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("linear: list_issues: server repeated cursor %q", next)
		}
		seen[next] = true
		args["cursor"] = next
	}
}

func (c *Client) Issue(ctx context.Context, key string) (tracker.IssueDetail, error) {
	b, err := c.call(ctx, "get_issue", map[string]any{"id": key})
	if err != nil {
		return tracker.IssueDetail{}, err
	}
	d, err := parseIssue(b)
	if err != nil {
		return d, fmt.Errorf("linear: get_issue: %w", err)
	}
	return d, nil
}

func (c *Client) Users(ctx context.Context) ([]tracker.User, error) {
	var all []tracker.User
	args := map[string]any{"limit": 250}
	seen := map[string]bool{}
	for {
		b, err := c.call(ctx, "list_users", args)
		if err != nil {
			return nil, err
		}
		users, next, err := parseUsers(b)
		if err != nil {
			return nil, fmt.Errorf("linear: list_users: %w", err)
		}
		all = append(all, users...)
		if next == "" {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("linear: list_users: server repeated cursor %q", next)
		}
		seen[next] = true
		args["cursor"] = next
	}
}

// call invokes a tool and returns its concatenated text content.
func (c *Client) call(ctx context.Context, tool string, args map[string]any) ([]byte, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil { // a timeout or a broken connection: start afresh next time
		c.drop(s)
		return nil, fmt.Errorf("linear: %s: %w", tool, err)
	}
	var sb strings.Builder
	for _, ct := range r.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	if r.IsError {
		return nil, fmt.Errorf("linear: %s: %s", tool, sb.String())
	}
	return []byte(sb.String()), nil
}

type bearer struct {
	key  string
	next http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.next.RoundTrip(r)
}
