package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordsRoundTripAndPrune(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	a := Record{ID: "a1", Tool: "claude", TicketKey: "ABC-1", Pane: "%1", CreatedAt: time.Unix(1, 0).UTC()}
	b := Record{ID: "b2", Tool: "codex", TicketKey: "ABC-2", Pane: "%2"}
	for _, r := range []Record{a, b} {
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.All()
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	keep := s.Prune(all, map[string]bool{"%1": true})
	if len(keep) != 1 || keep[0] != a {
		t.Fatalf("keep=%+v", keep)
	}
	if all, _ := s.All(); len(all) != 1 {
		t.Fatalf("pruned record still on disk: %+v", all)
	}
	if a.Session() != "lanes-a1" {
		t.Fatal(a.Session())
	}
}

func TestCorruptRecordSkipped(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.Save(Record{ID: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "agents", "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := s.All()
	if len(all) != 1 || err == nil {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestLinks(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	l, err := s.Links()
	if err != nil || len(l) != 0 {
		t.Fatalf("l=%v err=%v", l, err)
	}
	l[LinkKey("claude", "s1")] = "ABC-3"
	if err := s.SaveLinks(l); err != nil {
		t.Fatal(err)
	}
	got, err := s.Links()
	if err != nil || got["claude:s1"] != "ABC-3" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	fi, _ := os.Stat(filepath.Join(s.Dir, "links.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestNewIDUnique(t *testing.T) {
	if a, b := NewID(), NewID(); a == b || len(a) != 8 {
		t.Fatalf("%q %q", a, b)
	}
}
