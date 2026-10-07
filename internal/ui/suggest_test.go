package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/tracker"
)

// transcript writes a fake Claude transcript where the user mentions keys n times each.
func transcript(t *testing.T, cfg, cwd, sid string, mentions map[string]int) {
	t.Helper()
	dir := filepath.Join(cfg, "projects", regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(cwd, "-"))
	os.MkdirAll(dir, 0o700)
	var b strings.Builder
	for k, n := range mentions {
		for range n {
			fmt.Fprintf(&b, `{"type":"user","message":{"content":"work on %s"}}`+"\n", k)
		}
	}
	os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(b.String()), 0o600)
}

func suggestModel(t *testing.T) *Model {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m, _ := controlModel(t)
	m.issues = []tracker.Issue{
		{Key: "ABC-1", Title: "fix it", Team: "Alpha", State: "Todo", StateType: "unstarted"},
		{Key: "ABC-2", Title: "other", Team: "Alpha", State: "Todo", StateType: "unstarted"},
	}
	m.agents = []agent.Agent{
		{ID: "s1", Tool: "claude", Name: "clear", Cwd: "/w/a", External: true},
		{ID: "s2", Tool: "claude", Name: "mixed", Cwd: "/w/b", External: true},
		{ID: "s3", Tool: "claude", Name: "silent", Cwd: "/w/c", External: true},
	}
	transcript(t, cfg, "/w/a", "s1", map[string]int{"ABC-1": 8, "ABC-2": 1})
	transcript(t, cfg, "/w/b", "s2", map[string]int{"ABC-1": 4, "ABC-2": 4})
	m.rebuild()
	return m
}

func TestSuggestReviewAndApply(t *testing.T) {
	m := suggestModel(t)
	_, cmd := m.Update(keyName("T"))
	m.Update(cmd())
	if m.review == nil || len(m.review.items) != 2 || m.review.skipped != 1 {
		t.Fatalf("review %+v", m.review)
	}
	v := view(m)
	if !strings.Contains(v, "[x] clear → ABC-1 fix it (8 mentions, 8 by you)") || !strings.Contains(v, "[ ] mixed") || !strings.Contains(v, "1 more sessions mention no open ticket") {
		t.Fatalf("view:\n%s", v)
	}
	m.Update(key("j"))                          // to "mixed"
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // other ticket, and check it
	m.Update(key("enter"))
	links, _ := m.opt.Store.Links()
	if links["claude:s1"] != "ABC-1" || links["claude:s2"] == "" {
		t.Fatalf("links %v", links)
	}
	if m.review != nil || !strings.Contains(m.notice, "linked 2 sessions") {
		t.Fatalf("notice %q", m.notice)
	}
	if strings.Contains(view(m), "Unlinked\n  ○ claude clear") {
		t.Fatal("clear still unlinked")
	}
}

func TestSuggestSpaceTogglesAndEscCancels(t *testing.T) {
	m := suggestModel(t)
	_, cmd := m.Update(keyName("T"))
	m.Update(cmd())
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // uncheck "clear"
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if links, _ := m.opt.Store.Links(); len(links) != 0 || m.review != nil {
		t.Fatalf("esc should change nothing: %v", links)
	}
	_, cmd = m.Update(keyName("T"))
	m.Update(cmd())
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m.Update(key("enter"))
	if links, _ := m.opt.Store.Links(); len(links) != 0 {
		t.Fatalf("unchecked item was linked: %v", links)
	}
}

func TestAutoLinkOnlyConfident(t *testing.T) {
	m := suggestModel(t)
	m.opt.Config.AutoLink = true
	runCmd2 := func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		if b, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range b {
				m.Update(c())
			}
		}
	}
	runCmd2(m.autoLink())
	links, _ := m.opt.Store.Links()
	if links["claude:s1"] != "ABC-1" || links["claude:s2"] != "" {
		t.Fatalf("links %v", links)
	}
	if cmd := m.autoLink(); cmd != nil { // within a minute: no rescans
		if b, ok := cmd().(tea.BatchMsg); ok && len(b) > 0 {
			t.Fatal("rescanned too soon")
		}
	}
	// a minute later the mixed session has become clearly about ABC-2
	transcript(t, os.Getenv("CLAUDE_CONFIG_DIR"), "/w/b", "s2", map[string]int{"ABC-1": 2, "ABC-2": 20})
	base := m.now()
	m.now = func() time.Time { return base.Add(2 * time.Minute) }
	runCmd2(m.autoLink())
	if links, _ := m.opt.Store.Links(); links["claude:s2"] != "ABC-2" {
		t.Fatalf("grown transcript not re-scored: %v", links)
	}
}

func TestAutoLinkRespectsUnlinkAndOtherAssignee(t *testing.T) {
	m := suggestModel(t)
	m.opt.Config.AutoLink = true
	m.links["claude:s1"] = "" // the user unlinked it on purpose
	if cmd := m.autoLink(); cmd != nil {
		if b, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range b {
				m.Update(c())
			}
		}
	}
	if m.links["claude:s1"] != "" {
		t.Fatal("re-linked a session the user unlinked")
	}
	m.links = map[string]string{}
	m.opt.Assignee = "someone-else"
	if cmd := m.autoLink(); cmd != nil {
		t.Fatal("auto_link ran while viewing another person's tickets")
	}
}

func endedTranscript(t *testing.T, cfg, sid, cwd, title string) {
	t.Helper()
	dir := filepath.Join(cfg, "projects", "somewhere")
	os.MkdirAll(dir, 0o700)
	body := fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"content":"hi"}}`+"\n"+`{"type":"ai-title","aiTitle":%q}`+"\n", cwd, title)
	os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600)
}

func TestEndedLinkedSessionsShowAndResume(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	endedTranscript(t, cfg, "dead1", t.TempDir(), "Fix the login redirect")
	ended := endedSessions(map[string]string{"claude:dead1": "ABC-1", "claude:alive": "ABC-1", "claude:nolog": "ABC-1", "claude:off": ""},
		[]agent.Agent{{ID: "alive", Tool: "claude"}})
	if len(ended) != 1 || ended[0].Name != "Fix the login redirect" || !ended[0].Ended || ended[0].TicketKey != "ABC-1" {
		t.Fatalf("ended %+v", ended)
	}

	m, ft := controlModel(t)
	m.opt.Adapters = []agent.Adapter{claudeLike{stopper{}}}
	m.links["claude:dead1"] = "ABC-1"
	snap := m.opt.Fleet.(*fleetSnap)
	snap.agents = []agent.Agent{snap.agents[2], ended[0]} // no lanes agents on ABC-1 now
	snap.live = nil
	m.Update(agentsMsg{seq: 99, agents: snap.agents})
	if v := view(m); !rowHas(v, "· Fix the login r", "ended") {
		t.Fatalf("view:\n%s", v)
	}
	// enter on the ticket offers: new agent, or resume the ended session
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("enter"))
	if m.modal == nil || len(m.modal.items) != 2 || !strings.Contains(m.modal.items[1].label, "Resume Fix the login redirect") {
		t.Fatalf("modal %+v", m.modal)
	}
	m.Update(key("down"))
	_, cmd := m.Update(key("enter")) // resume → plan built off the loop
	m.Update(cmd())
	if m.modal == nil || !strings.HasPrefix(m.modal.title, "Resume Fix the login redirect for ABC-1") {
		t.Fatalf("confirm %+v", m.modal)
	}
	_, cmd = m.Update(key("y"))
	m.Update(cmd())
	if !strings.Contains(strings.Join(ft.log, "|"), "new lanes-") {
		t.Fatalf("not started: %v", ft.log)
	}
	if _, ok := m.links["claude:dead1"]; ok {
		t.Fatal("resumed session's link not retired")
	}
}

func TestEnterOnEmptyTicketStartsNewAgent(t *testing.T) {
	m, _ := controlModel(t)
	snap := m.opt.Fleet.(*fleetSnap)
	snap.agents, snap.live = nil, nil
	m.Update(agentsMsg{seq: 99})
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.cursorTo(t, "ABC-1 fix it")
	_, cmd := m.Update(key("enter"))
	if cmd == nil || !strings.Contains(m.notice, "loading ABC-1") {
		t.Fatalf("enter on an empty ticket should start the new-agent flow; notice %q", m.notice)
	}
}

func TestAutoLinkSkipsUpForGrabs(t *testing.T) {
	m := suggestModel(t)
	m.opt.Config.AutoLink = true
	for i := range m.issues {
		if m.issues[i].Key == "ABC-1" {
			m.issues[i].Pool = true
		}
	}
	if cmd := m.autoLink(); cmd != nil {
		if b, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range b {
				m.Update(c())
			}
		}
	}
	if links, _ := m.opt.Store.Links(); links["claude:s1"] != "" {
		t.Fatalf("auto-linked to an up-for-grabs ticket: %v", links)
	}
	// mostly about an up-for-grabs ticket, a little about yours: not confidently yours
	m.autoChecked = map[string]autoSeen{}
	transcript(t, os.Getenv("CLAUDE_CONFIG_DIR"), "/w/b", "s2", map[string]int{"ABC-1": 20, "ABC-2": 6})
	if cmd := m.autoLink(); cmd != nil {
		if b, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range b {
				m.Update(c())
			}
		}
	}
	if links, _ := m.opt.Store.Links(); links["claude:s2"] != "" {
		t.Fatalf("linked a session that's mostly about an up-for-grabs ticket: %v", links)
	}
}
