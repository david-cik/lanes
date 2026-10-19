// Package mcp is `lanes mcp`: a Model Context Protocol server on stdin/stdout whose
// tools are the panel's control requests (agents, start, send, read, wait), so an
// agent can coordinate others with typed tools rather than shell commands.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// Call sends a control request to the panel and returns its JSON answer.
type Call func(op string, args map[string]any) ([]byte, error)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	op          string
	rename      map[string]string // tool argument → control argument
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

const agentArg = "the agent: its id from the agents tool, a prefix of it, or its ticket key when one agent is on that ticket"

var tools = []tool{
	{Name: "agents", op: "agents",
		Description: "The board's tickets (key, title, state, project) and the agents on each: status (working, idle, waiting, ended), ready (finished a turn nobody looked at), waiting (what it needs from the user), recent activity. Also returns seq, for wait.",
		InputSchema: schema(map[string]any{"project": str("only this project's tickets")})},
	{Name: "start", op: "start", rename: map[string]string{"instructions": "text"},
		Description: "Ask the user to start an agent on a ticket. The lanes panel shows them the start screen and they decide; this returns at once, and wait reports a \"started\" event with the new agent's id if they say yes.",
		InputSchema: schema(map[string]any{"ticket": str("the ticket key, e.g. ABC-12"),
			"instructions": str("added after the agent's first prompt: what to do, what other agents own")}, "ticket")},
	{Name: "send", op: "send",
		Description: "Type text into an agent lanes started and press Enter, as if the user typed it. Refused while the agent shows a prompt only the user can answer.",
		InputSchema: schema(map[string]any{"agent": str(agentArg), "text": str("what to tell it")}, "agent", "text")},
	{Name: "read", op: "read",
		Description: "What an agent wrote since its last prompt (its latest reply).",
		InputSchema: schema(map[string]any{"agent": str(agentArg)}, "agent")},
	{Name: "wait", op: "wait", rename: map[string]string{"timeout_seconds": "timeout"},
		Description: "Wait until an agent starts, finishes a turn (\"ready\"), needs the user (\"needs_you\"), or ends. Returns the events and a seq: pass it back as after next time so nothing is missed. Answers with timeout: true and no events if nothing happens in time.",
		InputSchema: schema(map[string]any{
			"after":           map[string]any{"type": "integer", "description": "the seq from the previous answer; omit for only new events"},
			"only":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "agent ids or ticket keys to wait for; omit for any"},
			"timeout_seconds": map[string]any{"type": "number", "description": "give up after this long (default 90)"},
		})},
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Serve answers MCP requests (one JSON-RPC message per line) until in ends.
func Serve(in io.Reader, out io.Writer, call Call, version string) error {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var r request
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == nil { // notifications need no answer
			continue
		}
		result, rpcErr := handle(r, call, version)
		msg := map[string]any{"jsonrpc": "2.0", "id": r.ID}
		if rpcErr != nil {
			msg["error"] = rpcErr
		} else {
			msg["result"] = result
		}
		if err := enc.Encode(msg); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handle(r request, call Call, version string) (any, map[string]any) {
	switch r.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(r.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "lanes", "version": version}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		d := json.NewDecoder(bytes.NewReader(r.Params))
		d.UseNumber() // a seq is too big to round-trip a float64 exactly
		if err := d.Decode(&p); err != nil {
			return nil, map[string]any{"code": -32602, "message": err.Error()}
		}
		for _, t := range tools {
			if t.Name == p.Name {
				return callTool(t, p.Arguments, call), nil
			}
		}
		return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name}
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + r.Method}
}

// callTool runs a tool; failures (no panel, a refused request) are tool errors the
// model sees, not protocol errors.
func callTool(t tool, in map[string]any, call Call) map[string]any {
	args := map[string]any{}
	for k, v := range in {
		if to, ok := t.rename[k]; ok {
			k = to
		}
		args[k] = v
	}
	text, isErr := "", false
	b, err := call(t.op, args)
	var e struct{ Error string }
	switch {
	case err != nil:
		text, isErr = err.Error(), true
	case json.Unmarshal(b, &e) == nil && e.Error != "":
		text, isErr = e.Error, true
	default:
		text = string(b)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}
