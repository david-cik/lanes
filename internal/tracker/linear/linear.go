// Package linear implements tracker.Tracker over Linear's hosted MCP server.
package linear

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-cik/lanes/internal/tracker"
)

const Endpoint = "https://mcp.linear.app/mcp"

var issueFields = []string{"id", "title", "url", "gitBranchName", "status", "statusType", "team", "updatedAt"}

type Client struct{ s *mcp.ClientSession }

var _ tracker.Tracker = (*Client)(nil)

// Dial connects to endpoint. With apiKey set it sends it as a Bearer token;
// otherwise oauth (may be nil) handles authorization.
func Dial(ctx context.Context, endpoint, apiKey string, oauth auth.OAuthHandler) (*Client, error) {
	t := &mcp.StreamableClientTransport{Endpoint: endpoint}
	if apiKey != "" {
		t.HTTPClient = &http.Client{Transport: bearer{apiKey, http.DefaultTransport}}
	} else {
		t.OAuthHandler = oauth
	}
	return Connect(ctx, t)
}

// Connect starts an MCP session over any transport (tests use in-memory ones).
func Connect(ctx context.Context, t mcp.Transport) (*Client, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "lanes", Version: "dev"}, nil)
	s, err := c.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("linear: connect: %w", err)
	}
	return &Client{s}, nil
}

func (c *Client) Close() error { return c.s.Close() }

func (c *Client) Issues(ctx context.Context, assignee string) ([]tracker.Issue, error) {
	var all []tracker.Issue
	args := map[string]any{"assignee": assignee, "fields": issueFields, "limit": 250}
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
		args["cursor"] = next
	}
}

func (c *Client) Users(ctx context.Context) ([]tracker.User, error) {
	var all []tracker.User
	args := map[string]any{"limit": 250}
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
		args["cursor"] = next
	}
}

// call invokes a tool and returns its concatenated text content.
func (c *Client) call(ctx context.Context, tool string, args map[string]any) ([]byte, error) {
	r, err := c.s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
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
