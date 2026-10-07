package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
)

func write(t *testing.T, dir, id, body string, age time.Duration) {
	p := filepath.Join(dir, "projects", "x", id+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte(body), 0o600)
	when := time.Now().Add(-age)
	os.Chtimes(p, when, when)
}

func TestListMatchResolveLink(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	write(t, cfg, "aaaa1111", `{"type":"user","cwd":"/src/app","message":{"content":"tighten the CSP for ABC-7 in /tmp/claude-502 with XTERM-256"}}`+"\n"+`{"type":"ai-title","aiTitle":"CSP rollout"}`+"\n", time.Hour)
	write(t, cfg, "bbbb2222", `{"type":"user","cwd":"/src","message":{"content":"old work"}}`+"\n", 30*24*time.Hour)
	write(t, cfg, "cccc3333", `{"type":"user","cwd":"/src","message":{"content":"running but old"}}`+"\n", 30*24*time.Hour)
	write(t, cfg, "dddd4444", `{"type":"user","cwd":"/src","entrypoint":"sdk-cli","message":{"content":"a plugin's session"}}`+"\n", time.Hour)
	store := state.Store{Dir: t.TempDir()}
	store.Save(state.Record{ID: "r1", SessionID: "cccc3333", TicketKey: "ABC-1"})

	all := List([]agent.Agent{{ID: "cccc3333"}}, 7, store)
	if len(all) != 2 || all[0].ID != "aaaa1111" || all[0].Title != "CSP rollout" || len(all[0].Mentions) != 1 || all[0].Mentions[0] != "ABC-7 ×1" {
		t.Fatalf("list %+v", all)
	}
	if all[1].ID != "cccc3333" || !all[1].Running || all[1].Ticket != "ABC-1" {
		t.Fatalf("running session %+v", all[1])
	}
	if m := Match(all, "csp app"); len(m) != 1 || m[0].ID != "aaaa1111" {
		t.Fatalf("match %+v", m)
	}
	if _, err := Resolve(all, "aa"); err == nil {
		t.Fatal("short prefix accepted")
	}
	s, err := Resolve(all, "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := Link(context.Background(), store, s.ID, "abc-7"); err != nil {
		t.Fatal(err)
	}
	if l, _ := store.Links(); l["claude:aaaa1111"] != "ABC-7" {
		t.Fatalf("links %v", l)
	}
	if err := Link(context.Background(), store, "cccc3333", state.NoTicket); err != nil {
		t.Fatal(err)
	}
	if recs, _ := store.All(); recs[0].TicketKey != state.NoTicket {
		t.Fatalf("launched agent's record not updated: %+v", recs)
	}
	if err := Link(context.Background(), store, "aaaa1111", "not a key"); err == nil {
		t.Fatal("bad ticket accepted")
	}
}

func TestLookup(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	write(t, cfg, "abcd1111", `{"type":"ai-title","aiTitle":"one"}`+"\n", time.Hour)
	write(t, cfg, "abcd2222", `{"type":"ai-title","aiTitle":"two"}`+"\n", 90*24*time.Hour)
	if _, err := Lookup("abcd", nil); err == nil {
		t.Fatal("ambiguous prefix accepted")
	}
	if s, err := Lookup("abcd2", nil); err != nil || s.Title != "two" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := Lookup("ab*", nil); err == nil {
		t.Fatal("glob accepted")
	}
}

func TestRunningSessionWithoutTranscript(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	running := []agent.Agent{{ID: "f0rk1234-5678", Name: "ABC-1 fork", Cwd: "/src"}}
	s, err := Lookup("f0rk", running)
	if err != nil || s.ID != "f0rk1234-5678" || !s.Running || s.Title != "ABC-1 fork" {
		t.Fatalf("lookup %+v %v", s, err)
	}
	all := List(running, 7, state.Store{Dir: t.TempDir()})
	if len(all) != 1 || all[0].ID != "f0rk1234-5678" || !all[0].Running {
		t.Fatalf("list %+v", all)
	}
}
