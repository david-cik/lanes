package hook

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sock(t *testing.T) string {
	// unix socket paths must be short; t.TempDir() can exceed the limit on macOS.
	d, err := os.MkdirTemp("/tmp", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "s")
}

func next(t *testing.T, s *Server) Event {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

func TestStatusEventIsFireAndForget(t *testing.T) {
	path := sock(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	Client(strings.NewReader(`{"x":1}`), &out, path, "a1", "claude", "Stop")
	ev := next(t, s)
	if ev.Agent != "a1" || ev.Event != "Stop" || string(ev.Payload) != `{"x":1}` || ev.Reply != nil || out.Len() != 0 {
		t.Fatalf("ev %+v out %q", ev, out.String())
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", fi.Mode().Perm())
	}
}

func TestBlockingRoundTrip(t *testing.T) {
	path := sock(t)
	s, _ := Listen(path)
	defer s.Close()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { Client(strings.NewReader(`{}`), &out, path, "a1", "claude", "PermissionRequest"); close(done) }()
	ev := next(t, s)
	ev.Reply([]byte(`{"decision":"x"}`))
	ev.Reply([]byte(`ignored`))
	<-done
	if out.String() != `{"decision":"x"}` {
		t.Fatalf("out %q", out.String())
	}
}

func TestPanelClosingMidWaitPrintsNothing(t *testing.T) {
	path := sock(t)
	s, _ := Listen(path)
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { Client(strings.NewReader(`{}`), &out, path, "a1", "claude", "PermissionRequest"); close(done) }()
	next(t, s)
	s.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client still waiting after panel closed")
	}
	if out.Len() != 0 {
		t.Fatalf("out %q", out.String())
	}
}

func TestClientHangupReportsClosed(t *testing.T) {
	path := sock(t)
	s, _ := Listen(path)
	defer s.Close()
	done := make(chan struct{})
	go func() {
		// a client that gives up: send, then close without waiting
		Client(strings.NewReader(`{}`), &bytes.Buffer{}, path, "a1", "claude", "Stop")
		close(done)
	}()
	next(t, s)
	<-done
	// now a blocking one whose process dies: simulate by dialing and closing
	c := rawBlocking(t, path)
	ev := next(t, s)
	c.Close()
	if closed := next(t, s); !closed.Closed || closed.ID != ev.ID {
		t.Fatalf("closed %+v for %+v", closed, ev)
	}
}

func TestNoPanelIsSilent(t *testing.T) {
	var out bytes.Buffer
	Client(strings.NewReader(`{}`), &out, filepath.Join(t.TempDir(), "none"), "a1", "claude", "PermissionRequest")
	Client(strings.NewReader(`{}`), &out, "", "a1", "claude", "PermissionRequest")
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}

func TestSecondListenerRefused(t *testing.T) {
	path := sock(t)
	s, _ := Listen(path)
	defer s.Close()
	if _, err := Listen(path); err == nil {
		t.Fatal("second panel accepted")
	}
	s.Close()
	s2, err := Listen(path) // stale file after close is replaced
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func rawBlocking(t *testing.T, path string) interface{ Close() error } {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"agent":"a2","tool":"claude","event":"PermissionRequest","payload":{}}` + "\n"))
	return c
}
