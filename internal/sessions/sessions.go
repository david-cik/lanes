// Package sessions finds Claude sessions — running or recent — and links them to
// tickets. It backs `lanes sessions` / `lanes link` and the panel's "ask Claude".
package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/suggest"
)

type Session struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Cwd      string    `json:"cwd"`
	When     time.Time `json:"last_activity"`
	Running  bool      `json:"running"`
	Ticket   string    `json:"ticket,omitempty"`   // current link ("-" = removed from tickets)
	Mentions []string  `json:"mentions,omitempty"` // ticket keys it mentions most, e.g. "ABC-1 ×12"
	First    string    `json:"first_prompt,omitempty"`
	Prompts  []string  `json:"recent_prompts,omitempty"`
}

func configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// List returns running sessions plus those whose transcript changed in the last days,
// newest first. running comes from the Claude adapter (may be nil).
func List(running []agent.Agent, days int, store state.Store) []Session {
	links, _ := store.Links()
	recs, _ := store.All()
	recTicket := map[string]string{}
	prefixes := map[string]bool{} // team keys this user links to, e.g. SRE
	for _, r := range recs {
		recTicket[r.SessionID] = r.TicketKey
		addPrefix(prefixes, r.TicketKey)
	}
	for _, t := range links {
		addPrefix(prefixes, t)
	}
	alive := map[string]bool{}
	for _, a := range running {
		alive[a.ID] = true
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	paths, _ := filepath.Glob(filepath.Join(configDir(), "projects", "*", "*.jsonl"))
	seen := map[string]bool{}
	var out []Session
	add := func(id, path string) {
		if seen[id] {
			return
		}
		in, err := suggest.SessionInfo(path)
		if err != nil || (!alive[id] && in.When.Before(cutoff)) {
			return
		}
		// Sessions started by tools and plugins (Agent SDK, `claude -p`) aren't work a
		// person would put on a ticket.
		if strings.HasPrefix(in.Entrypoint, "sdk") {
			return
		}
		seen[id] = true
		s := Session{ID: id, Title: in.Title, Cwd: in.Cwd, When: in.When, Running: alive[id], First: in.First}
		if t, ok := recTicket[id]; ok {
			s.Ticket = t
		} else {
			s.Ticket = links[state.LinkKey("claude", id)]
		}
		if r, err := suggest.FileKeys(path, prefixes); err == nil {
			for _, c := range r[:min(3, len(r))] {
				s.Mentions = append(s.Mentions, fmt.Sprintf("%s ×%d", c.Key, c.Mentions))
			}
		}
		s.Prompts = suggest.Prompts(path, 3)
		out = append(out, s)
	}
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if fi, err := os.Stat(p); err == nil && (alive[id] || fi.ModTime().After(cutoff)) {
			add(id, p)
		}
	}
	slices.SortFunc(out, func(a, b Session) int { return b.When.Compare(a.When) })
	return out
}

// Match keeps sessions whose id, title, folder, prompts, or mentions contain every
// word of query (case-insensitive).
func Match(all []Session, query string) []Session {
	words := strings.Fields(strings.ToLower(query))
	var out []Session
	for _, s := range all {
		hay := strings.ToLower(strings.Join(append([]string{s.ID, s.Title, s.Cwd, s.Ticket, s.First}, append(s.Prompts, s.Mentions...)...), " "))
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(hay, w)
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// Resolve finds one session by id or id prefix (at least 4 characters).
func Resolve(all []Session, ref string) (Session, error) {
	if len(ref) < 4 {
		return Session{}, fmt.Errorf("session id %q is too short (use at least 4 characters)", ref)
	}
	var hits []Session
	for _, s := range all {
		if strings.HasPrefix(s.ID, ref) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return Session{}, fmt.Errorf("no recent session matches %q (lanes sessions lists them)", ref)
	case 1:
		return hits[0], nil
	}
	return Session{}, fmt.Errorf("%q matches %d sessions; use more of the id", ref, len(hits))
}

var keyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`)

func addPrefix(set map[string]bool, ticket string) {
	if p, _, ok := strings.Cut(ticket, "-"); ok && p != "" {
		set[strings.ToUpper(p)] = true
	}
}

// Link puts a session on ticket (or, with ticket == state.NoTicket, removes it from
// any). A session lanes launched has its launch record changed instead of a link.
func Link(ctx context.Context, store state.Store, sessionID, ticket string) error {
	ticket = strings.ToUpper(strings.TrimSpace(ticket))
	if ticket != state.NoTicket && !keyRe.MatchString(ticket) {
		return fmt.Errorf("%q doesn't look like a ticket key (e.g. ABC-123)", ticket)
	}
	recs, _ := store.All()
	for _, r := range recs {
		if r.SessionID == sessionID {
			_, err := store.SetRecordTicket(r.ID, ticket)
			return err
		}
	}
	_, err := store.SetLink(state.LinkKey("claude", sessionID), ticket)
	return err
}
