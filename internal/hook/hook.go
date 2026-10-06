// Package hook relays agent-tool hook events to the lanes panel over a unix socket.
//
// The tool runs `lanes hook <tool> <event>` with the payload on stdin. The client
// sends one JSON line {agent, tool, event, payload}. For blocking events it then waits
// for one reply line {stdout}, prints stdout, and exits. Every failure (no panel,
// panel gone mid-wait, bad reply) ends with exit 0 and no output, so the tool carries
// on exactly as if lanes weren't there.
package hook

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const maxPayload = 1 << 20

// Blocking events wait for the panel's answer.
var Blocking = map[string]bool{"PermissionRequest": true}

type message struct {
	Agent   string          `json:"agent"`
	Tool    string          `json:"tool"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

type reply struct {
	Stdout string `json:"stdout"`
}

// Client is `lanes hook`. agentID and socket come from LANES_AGENT_ID / LANES_SOCKET.
// With global set (hooks from `lanes install-hooks`), the agent is "<tool>:<session_id>"
// from the payload, and agents lanes launched are skipped: they report through their
// own injected hooks, so answering twice would double every event.
func Client(stdin io.Reader, stdout io.Writer, socket, agentID, tool, event string, global bool) {
	if global {
		if agentID != "" {
			return
		}
	} else if agentID == "" {
		return
	}
	if socket == "" {
		return
	}
	payload, _ := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if !json.Valid(payload) {
		payload = []byte("null")
	}
	if global {
		var p struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		agentID = tool + ":" + p.SessionID
	}
	conn, err := net.DialTimeout("unix", socket, 500*time.Millisecond)
	if err != nil {
		return
	}
	defer conn.Close()
	line, _ := json.Marshal(message{agentID, tool, event, payload})
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil || !Blocking[event] {
		return
	}
	conn.SetWriteDeadline(time.Time{})
	b, err := bufio.NewReaderSize(conn, maxPayload).ReadBytes('\n')
	if err != nil {
		return
	}
	var r reply
	if json.Unmarshal(b, &r) == nil && r.Stdout != "" {
		io.WriteString(stdout, r.Stdout)
	}
}

// Event is one hook call received by the panel.
type Event struct {
	ID      int // unique per connection
	Agent   string
	Tool    string
	Event   string
	Payload []byte
	// Reply answers a blocking event (stdout may be empty = no decision). It is nil for
	// non-blocking events. Only the first call has any effect.
	Reply func(stdout []byte)
	// Closed is true for the synthetic event sent when a blocking connection ends
	// before it was answered (the hook timed out or the tool moved on).
	Closed bool
}

type Server struct {
	ln     net.Listener
	events chan Event
	mu     sync.Mutex
	open   map[int]net.Conn
	next   int
	done   chan struct{}
	closed sync.Once
}

// Listen serves on path. A stale socket file is replaced; a live one means another
// panel is running.
func Listen(path string) (*Server, error) {
	if len(path) > 100 {
		return nil, fmt.Errorf("socket path too long for unix sockets (%d bytes): %s", len(path), path)
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("another lanes panel is listening on %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{ln: ln, events: make(chan Event, 64), open: map[int]net.Conn{}, done: make(chan struct{})}
	go s.accept()
	return s, nil
}

func (s *Server) Events() <-chan Event { return s.events }

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := bufio.NewReaderSize(c, maxPayload+4096).ReadBytes('\n')
	var m message
	if err != nil || json.Unmarshal(b, &m) != nil || m.Agent == "" {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	s.mu.Lock()
	s.next++
	id := s.next
	s.mu.Unlock()
	ev := Event{ID: id, Agent: m.Agent, Tool: m.Tool, Event: m.Event, Payload: m.Payload}
	if !Blocking[m.Event] {
		c.Close()
		s.send(ev)
		return
	}
	s.mu.Lock()
	select {
	case <-s.done: // closing: hang up now rather than register a conn Close won't see
		s.mu.Unlock()
		c.Close()
		return
	default:
	}
	s.open[id] = c
	s.mu.Unlock()
	var once sync.Once
	answered := make(chan struct{})
	ev.Reply = func(stdout []byte) {
		once.Do(func() {
			line, _ := json.Marshal(reply{string(stdout)})
			c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			c.Write(append(line, '\n'))
			close(answered)
			s.drop(id)
		})
	}
	s.send(ev)
	// The client never writes again; a read returning means it hung up.
	io.Copy(io.Discard, c)
	select {
	case <-answered:
	default:
		once.Do(func() { s.drop(id) })
		s.send(Event{ID: id, Agent: m.Agent, Tool: m.Tool, Event: m.Event, Closed: true})
	}
}

func (s *Server) drop(id int) {
	s.mu.Lock()
	if c, ok := s.open[id]; ok {
		c.Close()
		delete(s.open, id)
	}
	s.mu.Unlock()
}

func (s *Server) send(ev Event) {
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// Close stops listening and hangs up on every waiting hook (they then exit with no
// decision, leaving the tool's own prompt in charge).
func (s *Server) Close() error {
	var err error
	s.closed.Do(func() {
		s.mu.Lock()
		close(s.done)
		s.mu.Unlock()
		err = s.ln.Close()
		s.mu.Lock()
		for id, c := range s.open {
			c.Close()
			delete(s.open, id)
		}
		s.mu.Unlock()
	})
	return err
}
