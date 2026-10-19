// Package sessions finds agent sessions — running or recent — and links them to
// tickets. It backs `lanes sessions` / `lanes link` and the panel's "ask Claude".
package sessions

import (
	"context"
	"fmt"
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
	Tool     string    `json:"tool"`
	Title    string    `json:"title"`
	Cwd      string    `json:"cwd"`
	When     time.Time `json:"last_activity"`
	Running  bool      `json:"running"`
	Ticket   string    `json:"ticket,omitempty"`   // current link ("-" = removed from tickets)
	Mentions []string  `json:"mentions,omitempty"` // ticket keys it mentions most, e.g. "ABC-1 ×12"
	First    string    `json:"first_prompt,omitempty"`
	Prompts  []string  `json:"recent_prompts,omitempty"`
}

// List returns running sessions plus those whose transcript changed in the last days,
// newest first, of every tool in ads whose transcripts lanes can read. running comes
// from the adapters (may be nil).
func List(ads []agent.Adapter, running []agent.Agent, days int, store state.Store) []Session {
	links, _ := store.Links()
	recs, _ := store.All()
	recTicket := map[string]string{} // by tool:session
	prefixes := map[string]bool{}    // team keys this user links to, e.g. SRE
	for _, r := range recs {
		recTicket[state.LinkKey(r.Tool, r.SessionID)] = r.TicketKey
		addPrefix(prefixes, r.TicketKey)
	}
	for _, t := range links {
		addPrefix(prefixes, t)
	}
	ticketOf := func(k string) string {
		if t, ok := recTicket[k]; ok {
			return t
		}
		return links[k]
	}
	alive := map[string]agent.Agent{} // by tool:session
	for _, a := range running {
		alive[state.LinkKey(a.Tool, a.ID)] = a
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	seen := map[string]bool{}
	var out []Session
	for _, ad := range ads {
		tr, ok := ad.(agent.Transcripts)
		if !ok {
			continue
		}
		paths := tr.Transcripts(cutoff)
		for _, a := range alive { // running ones, however long ago they last wrote
			if a.Tool == ad.Name() {
				if p := tr.TranscriptPath(a.Cwd, a.ID); p != "" {
					paths[a.ID] = p
				}
			}
		}
		for id, path := range paths {
			k := state.LinkKey(ad.Name(), id)
			seen[k] = true // also for skipped ones, so the running list below doesn't add them back
			in, err := tr.SessionInfo(path)
			_, running := alive[k]
			if err != nil || (!running && in.When.Before(cutoff)) || in.Background {
				continue // a session a tool or plugin started isn't work to put on a ticket
			}
			s := Session{ID: id, Tool: ad.Name(), Title: in.Title, Cwd: in.Cwd, When: in.When, Running: running,
				First: in.First, Ticket: ticketOf(k)}
			r := suggest.FileKeys(tr, path, prefixes)
			for _, c := range r[:min(3, len(r))] {
				s.Mentions = append(s.Mentions, fmt.Sprintf("%s ×%d", c.Key, c.Mentions))
			}
			s.Prompts = suggest.Prompts(tr, path, 3)
			out = append(out, s)
		}
	}
	for k, a := range alive { // running but no transcript yet, e.g. a fork that hasn't taken a turn
		if !seen[k] {
			out = append(out, Session{ID: a.ID, Tool: a.Tool, Title: a.Name, Cwd: a.Cwd, When: a.Since, Running: true, Ticket: ticketOf(k)})
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

// Lookup finds one session by id or id prefix among the transcripts of every tool in
// ads, or a running session that has none yet.
func Lookup(ads []agent.Adapter, ref string, running []agent.Agent) (Session, error) {
	if len(ref) < 4 || strings.ContainsAny(ref, "/*?[") {
		return Session{}, fmt.Errorf("session id %q is too short or not an id (use at least 4 characters)", ref)
	}
	found := map[string]Session{} // by tool:session
	for _, a := range running {
		if strings.HasPrefix(a.ID, ref) {
			found[state.LinkKey(a.Tool, a.ID)] = Session{ID: a.ID, Tool: a.Tool, Title: a.Name, Cwd: a.Cwd, When: a.Since, Running: true}
		}
	}
	for _, ad := range ads {
		tr, ok := ad.(agent.Transcripts)
		if !ok {
			continue
		}
		for id, p := range tr.Transcripts(time.Time{}) {
			if !strings.HasPrefix(id, ref) {
				continue
			}
			k := state.LinkKey(ad.Name(), id)
			in, _ := tr.SessionInfo(p)
			found[k] = Session{ID: id, Tool: ad.Name(), Title: in.Title, Cwd: in.Cwd, When: in.When, Running: found[k].Running}
		}
	}
	switch len(found) {
	case 0:
		return Session{}, fmt.Errorf("no session matches %q (lanes sessions lists them)", ref)
	case 1:
		for _, s := range found {
			return s, nil
		}
	}
	return Session{}, fmt.Errorf("%q matches %d sessions; use more of the id", ref, len(found))
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
func Link(ctx context.Context, store state.Store, tool, sessionID, ticket string) error {
	ticket = strings.ToUpper(strings.TrimSpace(ticket))
	if ticket != state.NoTicket && !keyRe.MatchString(ticket) {
		return fmt.Errorf("%q doesn't look like a ticket key (e.g. ABC-123)", ticket)
	}
	recs, _ := store.All()
	for _, r := range recs {
		if r.Tool == tool && r.SessionID == sessionID {
			_, err := store.SetRecordTicket(r.ID, ticket)
			return err
		}
	}
	_, err := store.SetLink(state.LinkKey(tool, sessionID), ticket)
	return err
}
