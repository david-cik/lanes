package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/config"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/state"
	"github.com/david-cik/lanes/internal/tracker"
)

// ctl sends control request op with args and returns where its answer lands (nil
// until answered) and the command it returned.
func ctl(m *Model, id int, op, args string) (*map[string]any, tea.Cmd) {
	got := new(map[string]any)
	ev := hook.Event{ID: id, Control: op, Payload: []byte(args), Reply: func(b []byte) {
		if *got == nil {
			json.Unmarshal(b, got)
		}
	}}
	_, cmd := m.Update(hookMsg(ev))
	return got, cmd
}

func TestControlAgentsListsTicketsAndAgents(t *testing.T) {
	m, _, _ := hookModel(t)
	event(m, "r1", "UserPromptSubmit")
	got, _ := ctl(m, 1, "agents", `{}`)
	b, _ := json.Marshal(*got)
	s := string(b)
	for _, want := range []string{`"key":"ABC-1"`, `"id":"r1"`, `"id":"r2"`, `"status":"working"`, `"id":"claude:x9"`, `"lanes":false`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	got, _ = ctl(m, 2, "agents", `{"project":"Nope"}`)
	if b, _ := json.Marshal(*got); !strings.Contains(string(b), `"tickets":[]`) || !strings.Contains(string(b), `"unlinked":[]`) {
		t.Fatalf("%s", b)
	}
}

func TestControlWait(t *testing.T) {
	m, _, _ := hookModel(t)
	only2, _ := ctl(m, 1, "wait", `{"only":["r2"]}`)
	self1, _ := ctl(m, 9, "wait", `{"self":"r1"}`) // r1's own events aren't news to it
	any1, _ := ctl(m, 2, "wait", `{}`)
	if *any1 != nil {
		t.Fatalf("answered before anything happened: %v", *any1)
	}
	event(m, "r1", "UserPromptSubmit")
	event(m, "r1", "Stop")
	evs, _ := (*any1)["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["kind"] != "ready" || evs[0].(map[string]any)["ticket"] != "ABC-1" {
		t.Fatalf("%v", *any1)
	}
	if *only2 != nil || *self1 != nil {
		t.Fatalf("woke for another agent, or itself: %v %v", *only2, *self1)
	}
	seq := int((*any1)["seq"].(float64))
	// Missed while not waiting: a wait from an earlier seq answers at once.
	again, _ := ctl(m, 3, "wait", `{"after":`+itoa(seq-1)+`}`)
	if *again == nil {
		t.Fatal("event before the wait was lost")
	}
	m.Update(hookMsg(hook.Event{ID: 1, Control: "wait", Closed: true})) // that caller hung up
	if _, ok := m.waiters[1]; ok {
		t.Fatal("hung-up waiter kept")
	}
	late, _ := ctl(m, 4, "wait", `{"timeout":0.01}`)
	m.Update(ctlTimeoutMsg{4})
	if (*late)["timeout"] != true || int((*late)["seq"].(float64)) != seq {
		t.Fatalf("%v", *late)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestControlSend(t *testing.T) {
	m, ft, _ := hookModel(t)
	got, cmd := ctl(m, 1, "send", `{"agent":"r1","text":"carry on"}`)
	runCmd(cmd)
	if (*got)["sent"] != true || ft.log[len(ft.log)-1] != "text %1 carry on" {
		t.Fatalf("%v %v", *got, ft.log)
	}
	for ref, want := range map[string]string{"ABC-1": "matches 2 agents", "claude:x9": "not started by lanes", "zzz": "no agent matches"} {
		got, _ := ctl(m, 2, "send", `{"agent":"`+ref+`","text":"hi"}`)
		if e, _ := (*got)["error"].(string); !strings.Contains(e, want) {
			t.Fatalf("%s: %v", ref, *got)
		}
	}
}

func TestControlRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "projects", "w"), 0o700)
	os.WriteFile(filepath.Join(dir, "projects", "w", "s1.jsonl"), []byte(
		`{"type":"user","message":{"content":"go"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"all done"}]}}`+"\n"), 0o600)
	m, _, _ := hookModel(t)
	got, cmd := ctl(m, 1, "read", `{"agent":"r1"}`)
	runCmd(cmd)
	if (*got)["reply"] != "all done" || (*got)["ticket"] != "ABC-1" {
		t.Fatalf("%v", *got)
	}
}

func TestControlStartAsksTheUser(t *testing.T) {
	m, ft, notes := hookModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	waiting, _ := ctl(m, 1, "wait", `{}`)
	got, start := ctl(m, 2, "start", `{"ticket":"abc-1","text":"only the DNS part"}`)
	if (*got)["asked"] != true {
		t.Fatalf("%v", *got)
	}
	// detail → folder plan → the usual confirmation, saying who asked and what it said
	feedLaunch(m, start)
	text := strings.Join(m.modal.lines, "\n")
	if !strings.Contains(text, "asked by: another agent") || !strings.Contains(text, "told:     only the DNS part") {
		t.Fatalf("modal %q", text)
	}
	if len(*notes) == 0 || !strings.Contains((*notes)[len(*notes)-1], "asks to start work on ABC-1") {
		t.Fatalf("notes %v", *notes)
	}
	_, cmd := m.Update(key("y"))
	m.Update(cmd()) // launchedMsg
	if !strings.Contains(strings.Join(ft.argv, " "), "only the DNS part") {
		t.Fatalf("instructions not in the agent's prompt: %q", ft.argv)
	}
	evs, _ := (*waiting)["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["kind"] != "started" {
		t.Fatalf("%v", *waiting)
	}
}

// feedLaunch runs cmd and feeds the launch-flow messages it produces back in.
func feedLaunch(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			feedLaunch(m, c)
		}
	case detailMsg, planMsg:
		_, next := m.Update(msg)
		feedLaunch(m, next)
	}
}

func TestControlWaitAfterPanelRestart(t *testing.T) {
	m, _, _ := hookModel(t)
	event(m, "r1", "UserPromptSubmit")
	event(m, "r1", "Stop")
	got, _ := ctl(m, 1, "wait", `{"after":57}`) // a seq from the panel before this one (they start lower)
	if evs, _ := (*got)["events"].([]any); len(evs) != 1 {
		t.Fatalf("%v", *got)
	}
}

func TestStaleStartNoteIgnored(t *testing.T) {
	m, _, _ := hookModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.startNote["ABC-1"] = startNote{"old instructions", m.now().Add(-time.Hour)}
	m.cursorTo(t, "ABC-1 fix it")
	_, cmd := m.Update(key("n"))
	feedLaunch(m, cmd)
	if text := strings.Join(m.modal.lines, "\n"); strings.Contains(text, "asked by") || strings.Contains(text, "old instructions") {
		t.Fatalf("modal %q", text)
	}
}

func TestCoordinatorStartsOncePerProject(t *testing.T) {
	m, ft, _ := hookModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	m.issues[0].Project = "Apollo"
	m.rebuild()
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("C"))
	if m.modal == nil || !strings.Contains(m.modal.title, "coordinator for Apollo") {
		t.Fatalf("modal %+v", m.modal)
	}
	_, cmd := m.Update(key("y"))
	m.Update(cmd()) // launchedMsg
	argv := strings.Join(ft.argv, "\n")
	if !strings.Contains(argv, "--allowedTools=mcp__lanes__agents,mcp__lanes__send,mcp__lanes__read,mcp__lanes__wait,mcp__lanes__deny") ||
		!strings.Contains(argv, "--permission-mode=manual") || !strings.Contains(argv, `--mcp-config={"mcpServers":{"lanes":{"args":["mcp"],"command":"lanes","type":"stdio"}}}`) ||
		!strings.Contains(argv, `calling agents for "Apollo"`) || !strings.Contains(argv, "coordinator · Apollo") {
		t.Fatalf("argv:\n%s", argv)
	}
	if !strings.Contains(m.notice, "started the coordinator of Apollo") || m.live[len(m.live)-1].Project != "Apollo" {
		t.Fatalf("notice %q live %+v", m.notice, m.live)
	}
	m.Update(agentsMsg{agents: append(slices.Clone(m.agents), agent.Agent{ID: "c", Tool: "claude", Name: "coordinator · Apollo",
		TicketKey: state.NoTicket, RecordID: m.live[len(m.live)-1].ID, Project: "Apollo", Status: agent.Idle}), live: m.live})
	if v := view(m); !strings.Contains(v, "COORDINATORS") || !rowHas(v, "○ Apollo", "idle") {
		t.Fatalf("view:\n%s", v)
	}
	m.modal = nil
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("C")) // one per project: shows the one there is
	if m.modal != nil || !strings.Contains(m.notice, "already has its coordinator") {
		t.Fatalf("modal %+v notice %q", m.modal, m.notice)
	}
}

func TestCoordinatorNeedsAProject(t *testing.T) {
	m, _, _ := hookModel(t)
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("C"))
	if m.modal != nil || !strings.Contains(m.notice, "select a project") {
		t.Fatalf("modal %+v notice %q", m.modal, m.notice)
	}
}

func TestControlSendRefusedWhileAPromptIsOpen(t *testing.T) {
	m, ft, _ := hookModel(t)
	permission(m, 7, "r1")
	got, cmd := ctl(m, 1, "send", `{"agent":"r1","text":"go ahead"}`)
	runCmd(cmd)
	if e, _ := (*got)["error"].(string); !strings.Contains(e, "only the user can answer") || strings.Contains(strings.Join(ft.log, "|"), "go ahead") {
		t.Fatalf("%v %v", *got, ft.log)
	}
	m.Update(trustMsg{"r2", "%2"})
	got, _ = ctl(m, 2, "send", `{"agent":"r2","text":"go ahead"}`)
	if e, _ := (*got)["error"].(string); !strings.Contains(e, "only the user can answer") {
		t.Fatalf("trust prompt: %v", *got)
	}
}

func TestCoordinatorToolsFollowConfig(t *testing.T) {
	for _, c := range []struct{ start, approve, want string }{
		{"auto", "auto", "mcp__lanes__agents,mcp__lanes__send,mcp__lanes__read,mcp__lanes__wait,mcp__lanes__start,mcp__lanes__deny,mcp__lanes__approve"},
		{"panel", "off", "mcp__lanes__agents,mcp__lanes__send,mcp__lanes__read,mcp__lanes__wait,mcp__lanes__start\n"},
	} {
		m, ft, _ := hookModel(t)
		m.opt.Config.LaunchDir = t.TempDir()
		m.opt.Config.Coordinator = config.CoordinatorConfig{Start: c.start, Approve: c.approve}
		m.issues[0].Project = "Apollo"
		m.rebuild()
		m.cursorTo(t, "ABC-1 fix it")
		m.Update(key("C"))
		_, cmd := m.Update(key("y"))
		m.Update(cmd())
		if argv := strings.Join(ft.argv, "\n") + "\n"; !strings.Contains(argv, "--allowedTools="+c.want) || !strings.Contains(argv, "--permission-mode=manual") {
			t.Fatalf("%s/%s: %s", c.start, c.approve, argv)
		}
	}
}

// coordinated is hookModel with a running coordinator of project Apollo (record "c1").
func coordinated(t *testing.T) (*Model, *fakeTmux) {
	m, ft, _ := hookModel(t)
	m.opt.Config.LaunchDir = t.TempDir()
	m.opt.Tracker = detailTracker{&fakeTracker{}}
	m.issues[0].Project = "Apollo"
	m.live = append(m.live, state.Record{ID: "c1", Tool: "claude", TicketKey: state.NoTicket, Project: "Apollo", Pane: "%7"})
	m.rebuild()
	return m, ft
}

func TestCoordinatorStartsDirectly(t *testing.T) {
	m, ft := coordinated(t)
	shown := m.shown
	got, cmd := ctl(m, 1, "start", `{"ticket":"ABC-1","text":"only DNS","self":"c1"}`)
	if m.modal != nil {
		t.Fatalf("asked in the panel: %+v", m.modal)
	}
	m.Update(cmd()) // launchedMsg
	if (*got)["started"] != true || (*got)["agent"] == "" || (*got)["seq"] == nil || !strings.Contains(strings.Join(ft.argv, " "), "only DNS") {
		t.Fatalf("%v %q", *got, ft.argv)
	}
	if m.shown != shown {
		t.Fatalf("took the right pane: %q", m.shown)
	}
	m.opt.Config.Coordinator.Start = "panel"
	m.cursor = 0
	ctl(m, 2, "start", `{"ticket":"ABC-1","self":"c1"}`)
	got, _ = ctl(m, 3, "start", `{"ticket":"ABC-1"}`) // not a coordinator: the panel asks
	if (*got)["asked"] != true && !strings.Contains(fmt.Sprint(*got), "showing the user something else") {
		t.Fatalf("%v", *got)
	}
}

func TestControlApproveAndDeny(t *testing.T) {
	m, _ := coordinated(t)
	out := permission(m, 7, "r1")
	got, _ := ctl(m, 1, "approve", `{"agent":"r1","request":"Bash: rm -rf /","self":"c1"}`)
	if e, _ := (*got)["error"].(string); !strings.Contains(e, `waiting on "Bash: date > probe.txt"`) || len(*out) != 0 && (*out) != nil {
		t.Fatalf("%v %q", *got, *out)
	}
	got, _ = ctl(m, 2, "approve", `{"agent":"r1","request":"Bash:  date > probe.txt","self":"c1"}`)
	if (*got)["answered"] != "allow" || decisionOf(t, *out)["behavior"] != "allow" {
		t.Fatalf("%v %s", *got, *out)
	}
	out = permission(m, 8, "r2")
	got, _ = ctl(m, 3, "deny", `{"agent":"r2","request":"Bash: date > probe.txt","message":"not on this ticket","self":"c1"}`)
	if d := decisionOf(t, *out); (*got)["answered"] != "deny" || d["behavior"] != "deny" || d["message"] != "not on this ticket" {
		t.Fatalf("%v %v", *got, d)
	}
	out = permission(m, 10, "r1")
	for _, self := range []string{"", "r2"} { // anything but a coordinator
		got, _ = ctl(m, 5, "approve", `{"agent":"r1","request":"Bash: date > probe.txt","self":"`+self+`"}`)
		if e, _ := (*got)["error"].(string); !strings.Contains(e, "only a project's coordinator") || *out != nil {
			t.Fatalf("%q: %v %q", self, *got, *out)
		}
	}
	m.opt.Config.Coordinator.Approve = "off"
	permission(m, 9, "r1")
	got, _ = ctl(m, 4, "approve", `{"agent":"r1","request":"Bash: date > probe.txt","self":"c1"}`)
	if e, _ := (*got)["error"].(string); !strings.Contains(e, "is off") {
		t.Fatalf("%v", *got)
	}
}

func TestControlApprovesFolderTrust(t *testing.T) {
	m, ft := coordinated(t)
	m.Update(trustMsg{"r1", "%1"})
	ft.screen = "Quick safety check\n ❯ No, exit\n   Yes, I trust this folder"
	got, cmd := ctl(m, 1, "approve", `{"agent":"r1","request":"folder-trust prompt","self":"c1"}`)
	runCmd(cmd)
	if (*got)["answered"] != "trusted" || ft.log[len(ft.log)-1] != "keys %1 Down,Enter" {
		t.Fatalf("%v %v", *got, ft.log)
	}
}

func TestControlStop(t *testing.T) {
	m, ft := coordinated(t)
	delete(ft.sessions, "lanes-r1") // it exits when asked
	got, cmd := ctl(m, 1, "stop", `{"agent":"r1"}`)
	runCmd(cmd)
	if (*got)["stopped"] != true || !strings.Contains(strings.Join(ft.log, "|"), "text %1 /exit") || !m.stopping["r1"] {
		t.Fatalf("%v %v", *got, ft.log)
	}
	got, _ = ctl(m, 2, "stop", `{"agent":"claude:x9"}`)
	if e, _ := (*got)["error"].(string); !strings.Contains(e, "not started by lanes") {
		t.Fatalf("%v", *got)
	}
}

func TestCoordinatorKeptToItsProject(t *testing.T) {
	m, _ := coordinated(t)
	m.issues = append(m.issues, tracker.Issue{Key: "ABC-2", Title: "other", Team: "Alpha", State: "Todo", StateType: "unstarted", Project: "Zeus"})
	m.live = append(m.live, state.Record{ID: "r3", Tool: "claude", TicketKey: "ABC-2", Pane: "%3"})
	m.Update(agentsMsg{agents: append(slices.Clone(m.agents), agent.Agent{ID: "s3", Tool: "claude", Name: "three", TicketKey: "ABC-2", RecordID: "r3", Pane: "%3"}), live: m.live})
	permission(m, 7, "r3")
	for _, op := range []string{"approve", "stop"} {
		got, _ := ctl(m, 1, op, `{"agent":"r3","request":"Bash: date > probe.txt","self":"c1"}`)
		if e, _ := (*got)["error"].(string); !strings.Contains(e, "the project you coordinate") {
			t.Fatalf("%s: %v", op, *got)
		}
	}
}
