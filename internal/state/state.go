// Package state persists what lanes must remember across restarts: the agents it
// launched (one JSON file each) and manual links of external sessions to tickets.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Record describes an agent lanes launched.
type Record struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Name      string    `json:"name,omitempty"` // display name given at launch
	TicketKey string    `json:"ticket"`
	Repo      string    `json:"repo"`
	Worktree  string    `json:"worktree"`
	Branch    string    `json:"branch"`
	SessionID string    `json:"session_id"` // the tool's own session id
	Pane      string    `json:"pane"`       // tmux pane id, e.g. %12
	CreatedAt time.Time `json:"created_at"`
	// AdoptedFrom is the session this one was forked from (adopt or resume). The
	// original may keep running; the board hides it while this agent lives.
	AdoptedFrom string `json:"adopted_from,omitempty"`
}

// Session is the tmux session that is this agent's home.
func (r Record) Session() string { return "lanes-" + r.ID }

type Store struct{ Dir string }

func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s Store) agentsDir() string { return filepath.Join(s.Dir, "agents") }

func (s Store) Save(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.agentsDir(), r.ID+".json"), b)
}

func (s Store) Delete(id string) error {
	err := os.Remove(filepath.Join(s.agentsDir(), id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// All returns every record; unreadable files are skipped and reported in the error.
func (s Store) All() ([]Record, error) {
	entries, err := os.ReadDir(s.agentsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []Record
	var bad []error
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.agentsDir(), e.Name()))
		var r Record
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		recs = append(recs, r)
	}
	return recs, errors.Join(bad...)
}

// Prune keeps records whose agent still has a live tmux pane and deletes the rest.
// paneOf maps record ID → pane ID, found via the @lanes_agent pane option (pane IDs
// are reused after a tmux server restart, so they can't identify an agent alone).
// A record whose pane ID changed is updated. Callers must not prune when listing
// panes failed, or every record would be deleted.
func (s Store) Prune(recs []Record, paneOf map[string]string) ([]Record, error) {
	var keep []Record
	var errs []error
	for _, r := range recs {
		pane, ok := paneOf[r.ID]
		if !ok {
			errs = append(errs, s.Delete(r.ID))
			continue
		}
		if pane != r.Pane {
			r.Pane = pane
			errs = append(errs, s.Save(r))
		}
		keep = append(keep, r)
	}
	return keep, errors.Join(errs...)
}

// NoTicket, as a link or a launch record's ticket, keeps a session off every ticket:
// the user removed it, so matching by branch or name, L, and auto_link leave it alone.
const NoTicket = "-"

// Links maps "<tool>:<sessionId>" to a ticket key for manually linked external sessions.
type Links map[string]string

func LinkKey(tool, sessionID string) string { return tool + ":" + sessionID }

func (s Store) Links() (Links, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "links.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Links{}, nil
	}
	if err != nil {
		return Links{}, err
	}
	l := Links{}
	if err := json.Unmarshal(b, &l); err != nil {
		return Links{}, fmt.Errorf("links.json: %w", err)
	}
	return l, nil
}

func (s Store) SaveLinks(l Links) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, "links.json"), b)
}

// writeAtomic writes via a private temp file + rename so readers never see a partial file.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// locked runs fn holding an exclusive lock on the state dir, so the lanes CLI and the
// panel can't interleave read-modify-write updates.
func (s Store) locked(fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// editLinks applies change to the links on disk under the lock.
func (s Store) editLinks(change func(Links) bool) (Links, error) {
	var out Links
	err := s.locked(func() error {
		l, err := s.Links()
		if err != nil {
			return err
		}
		out = l
		if !change(l) {
			return nil
		}
		return s.SaveLinks(l)
	})
	return out, err
}

// SetLink changes one link, re-reading the file first so changes made meanwhile by
// another process (the lanes CLI, the panel) aren't lost.
func (s Store) SetLink(key, ticket string) (Links, error) {
	return s.editLinks(func(l Links) bool { l[key] = ticket; return true })
}

// SetLinkIfAbsent sets a link only if the session has none yet (not even a removal).
func (s Store) SetLinkIfAbsent(key, ticket string) (Links, bool, error) {
	set := false
	l, err := s.editLinks(func(l Links) bool {
		if _, ok := l[key]; ok {
			return false
		}
		l[key], set = ticket, true
		return true
	})
	return l, set, err
}

// DeleteLink removes one link, re-reading the file first (see SetLink).
func (s Store) DeleteLink(key string) (Links, error) {
	return s.editLinks(func(l Links) bool { delete(l, key); return true })
}

// SetRecordTicket changes the ticket of a launched agent's record on disk.
func (s Store) SetRecordTicket(id, ticket string) (Record, error) {
	var out Record
	err := s.locked(func() error {
		recs, _ := s.All()
		for _, r := range recs {
			if r.ID == id {
				r.TicketKey = ticket
				out = r
				return s.Save(r)
			}
		}
		return fmt.Errorf("no launched agent %s", id)
	})
	return out, err
}
