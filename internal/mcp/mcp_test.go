package mcp

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	var gotOp string
	var gotArgs map[string]any
	call := func(op string, args map[string]any) ([]byte, error) {
		gotOp, gotArgs = op, args
		switch op {
		case "send":
			return []byte(`{"error":"r1 is waiting on a prompt"}`), nil
		case "read":
			return nil, errors.New("no lanes panel is running")
		}
		return []byte(`{"events":[],"seq":1760000000123}`), nil
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wait","arguments":{"after":1760000000123,"timeout_seconds":5}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"send","arguments":{"agent":"r1","text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read","arguments":{"agent":"r1"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
	}, "\n")
	var out bytes.Buffer
	if err := Serve(strings.NewReader(in), &out, call, "v1"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 { // the notification gets no answer
		t.Fatalf("%d answers:\n%s", len(lines), out.String())
	}
	for i, want := range []string{
		`"protocolVersion":"2025-03-26"`,
		`"name":"start"`,
		`"isError":false`,
		`"text":"r1 is waiting on a prompt"`,
		`"text":"no lanes panel is running"`,
		`"code":-32601`,
	} {
		if !strings.Contains(lines[i], want) {
			t.Fatalf("answer %d lacks %s: %s", i+1, want, lines[i])
		}
	}
	if !strings.Contains(lines[3], `"isError":true`) || !strings.Contains(lines[4], `"isError":true`) {
		t.Fatalf("errors not flagged:\n%s", out.String())
	}
	if gotOp != "read" || gotArgs["agent"] != "r1" {
		t.Fatalf("%s %v", gotOp, gotArgs)
	}
}

func TestArgsRenamedAndSeqExact(t *testing.T) {
	var got map[string]any
	call := func(_ string, args map[string]any) ([]byte, error) { got = args; return []byte(`{}`), nil }
	Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait","arguments":{"after":1760000000123,"timeout_seconds":5}}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"start","arguments":{"ticket":"ABC-1","instructions":"only DNS"}}}`),
		&bytes.Buffer{}, call, "v1")
	if got["text"] != "only DNS" || got["ticket"] != "ABC-1" {
		t.Fatalf("start args %v", got)
	}
	Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait","arguments":{"after":1760000000123,"timeout_seconds":5}}}`),
		&bytes.Buffer{}, call, "v1")
	if n, ok := got["after"].(interface{ String() string }); !ok || n.String() != "1760000000123" || got["timeout"] == nil {
		t.Fatalf("wait args %v", got)
	}
}
