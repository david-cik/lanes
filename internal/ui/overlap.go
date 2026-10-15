package ui

import (
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/state"
)

// noteWrites records the files a lanes agent is changing and, when another agent on a
// different ticket already changed one of them, warns: two agents doing the same work.
// Files are compared within the same repo, relative to each agent's folder, so the same
// file in two worktrees counts as the same.
func (m *Model) noteWrites(id string, st *hookState, paths []string) tea.Cmd {
	i := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == id })
	if i < 0 {
		return nil // a session lanes didn't start: no folder to compare against
	}
	rec := m.live[i]
	if m.written == nil {
		m.written, m.warned = map[string][]string{}, map[string]bool{}
	}
	var cmd tea.Cmd
	for _, p := range paths {
		rel, err := filepath.Rel(rec.Worktree, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = p // outside its folder: compare the path itself
		}
		file := rec.Repo + "\x00" + rel
		for _, other := range m.written[file] {
			o := slices.IndexFunc(m.live, func(r state.Record) bool { return r.ID == other })
			pair := id + "\x00" + other + "\x00" + file
			if other == id || o < 0 || m.live[o].TicketKey == rec.TicketKey || m.warned[pair] {
				continue
			}
			m.warned[pair] = true
			st.overlap = m.live[o].TicketKey
			cmd = tea.Batch(cmd, m.ping(m.whoIs(id)+" is changing "+rel+", which "+m.whoIs(other)+" already changed — s tells it to stop"))
		}
		if !slices.Contains(m.written[file], id) {
			m.written[file] = append(m.written[file], id)
		}
	}
	return cmd // hook() rebuilds the board after this
}
