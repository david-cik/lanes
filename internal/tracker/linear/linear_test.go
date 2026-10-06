package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer serves canned list_issues pages keyed by cursor and records call args.
func fakeServer(t *testing.T, pages map[string]string, calls *[]map[string]any) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-linear"}, nil)
	s.AddTool(&mcp.Tool{Name: "list_issues", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			json.Unmarshal(req.Params.Arguments, &args)
			*calls = append(*calls, args)
			cursor, _ := args["cursor"].(string)
			body, ok := pages[cursor]
			if !ok {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad cursor"}}}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}}, nil
		})
	return s
}

func connectFake(t *testing.T, s *mcp.Server) *Client {
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c, err := Connect(ctx, ct)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestIssuesFollowsCursor(t *testing.T) {
	var calls []map[string]any
	c := connectFake(t, fakeServer(t, map[string]string{
		"":   `{"issues":[{"id":"ABC-1","statusType":"started"}],"hasNextPage":true,"cursor":"p2"}`,
		"p2": `{"issues":[{"id":"ABC-2","statusType":"unstarted"}],"hasNextPage":false}`,
	}, &calls))

	got, err := c.Issues(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Key != "ABC-2" {
		t.Fatalf("got %+v", got)
	}
	if len(calls) != 2 || calls[0]["assignee"] != "me" || calls[1]["cursor"] != "p2" {
		t.Fatalf("calls %+v", calls)
	}
	if f, _ := calls[0]["fields"].([]any); len(f) != len(issueFields) {
		t.Fatalf("fields not requested: %+v", calls[0])
	}
}

func TestToolErrorSurfaces(t *testing.T) {
	var calls []map[string]any
	c := connectFake(t, fakeServer(t, map[string]string{
		"": `{"issues":[],"hasNextPage":true,"cursor":"missing"}`,
	}, &calls))
	if _, err := c.Issues(context.Background(), "me"); err == nil {
		t.Fatal("want error from tool IsError")
	}
}

func TestDialSendsAPIKeyAsBearer(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("Authorization"):
		default:
		}
		http.Error(w, "stop", http.StatusTeapot)
	}))
	defer srv.Close()
	Dial(context.Background(), srv.URL, "lin_api_test", nil) // fails after the first request; header is what matters
	if h := <-got; h != "Bearer lin_api_test" {
		t.Fatalf("Authorization = %q", h)
	}
}

func TestRepeatedCursorStops(t *testing.T) {
	var calls []map[string]any
	c := connectFake(t, fakeServer(t, map[string]string{
		"":     `{"issues":[],"hasNextPage":true,"cursor":"loop"}`,
		"loop": `{"issues":[],"hasNextPage":true,"cursor":"loop"}`,
	}, &calls))
	if _, err := c.Issues(context.Background(), "me"); err == nil || len(calls) != 2 {
		t.Fatalf("err=%v calls=%d", err, len(calls))
	}
}
