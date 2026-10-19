package ui

import (
	"encoding/json"
	"slices"

	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
	"github.com/david-cik/lanes/internal/agent/claude"
	"github.com/david-cik/lanes/internal/hook"
	"github.com/david-cik/lanes/internal/state"
)

const permPayload = `{"tool_name":"Bash","tool_input":{"command":"date > probe.txt"},
 "permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"date > probe.txt"}],"behavior":"allow","destination":"localSettings"},
  {"type":"addDirectories","directories":["/w"],"destination":"session"}]}`

// hookModel is controlModel with the real Claude adapter, so decisions are real JSON.
func hookModel(t *testing.T) (*Model, *fakeTmux, *[]string) {
	m, ft := controlModel(t)
	m.opt.Adapters = []agent.Adapter{claude.New()}
	var notes []string
	m.opt.Notify = func(s string, _ bool) { notes = append(notes, s) }
	return m, ft, &notes
}

// permission sends a PermissionRequest for agent id and returns where its reply lands.
func permission(m *Model, evID int, id string) *[]byte {
	var got []byte
	got = nil
	out := &got
	replied := false
	_, cmd := m.Update(hookMsg(hook.Event{ID: evID, Agent: id, Tool: "claude", Event: "PermissionRequest", Payload: []byte(permPayload),
		Reply: func(b []byte) {
			if !replied {
				replied, *out = true, append([]byte{}, b...)
				if b == nil {
					*out = []byte{}
				}
			}
		}}))
	runCmd(cmd)
	return out
}

// runCmd executes a command and any batch it returns (side effects only).
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			runCmd(c)
		}
	}
}

func decisionOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var d struct {
		H struct {
			D map[string]any `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("reply %q: %v", b, err)
	}
	return d.H.D
}

func TestPermissionShowsOnRowAndHeader(t *testing.T) {
	m, _, notes := hookModel(t)
	permission(m, 1, "r1")
	v := view(m)
	if !rowHas(v, "⚠ one", "needs you") || !strings.Contains(v, "Bash: date > probe.txt") || !strings.Contains(v, "⚠ 1 waiting · a") || !strings.Contains(v, "ABC-1 fix it  ⚠") {
		t.Fatalf("view:\n%s", v)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "ABC-1 needs you") {
		t.Fatalf("notes %v", *notes)
	}
}

func TestApprovalKeys(t *testing.T) {
	cases := []struct {
		keys  []string
		check func(t *testing.T, d map[string]any)
	}{
		{[]string{"y"}, func(t *testing.T, d map[string]any) {
			if d["behavior"] != "allow" || d["updatedPermissions"] != nil {
				t.Fatalf("%v", d)
			}
		}},
		{[]string{"A"}, func(t *testing.T, d map[string]any) {
			up := d["updatedPermissions"].([]any)[0].(map[string]any)
			if up["destination"] != "localSettings" || up["type"] != "addRules" {
				t.Fatalf("%v", up)
			}
		}},
		{[]string{"tab", "s"}, func(t *testing.T, d map[string]any) {
			up := d["updatedPermissions"].([]any)[0].(map[string]any)
			if up["destination"] != "session" || up["type"] != "addDirectories" {
				t.Fatalf("%v", up)
			}
		}},
		{[]string{"n"}, func(t *testing.T, d map[string]any) {
			if d["behavior"] != "deny" {
				t.Fatalf("%v", d)
			}
		}},
	}
	for _, c := range cases {
		m, _, _ := hookModel(t)
		out := permission(m, 1, "r1")
		m.Update(key("a"))
		for _, k := range c.keys {
			m.Update(keyName(k))
		}
		c.check(t, decisionOf(t, *out))
		if m.waitingCount() != 0 || m.modal != nil {
			t.Fatalf("%v: still waiting or modal open", c.keys)
		}
	}
}

func TestEditRuleThenAlwaysAllow(t *testing.T) {
	m, _, _ := hookModel(t)
	out := permission(m, 1, "r1")
	m.Update(key("a"))
	m.Update(key("e"))
	m.modal.text = "date *"
	m.Update(key("enter")) // back to the approval modal
	m.Update(key("A"))
	up := decisionOf(t, *out)["updatedPermissions"].([]any)[0].(map[string]any)
	if r := up["rules"].([]any)[0].(map[string]any); r["ruleContent"] != "date *" {
		t.Fatalf("rule %v", r)
	}
}

func TestDenyWithMessage(t *testing.T) {
	m, _, _ := hookModel(t)
	out := permission(m, 1, "r1")
	m.Update(key("a"))
	m.Update(key("m"))
	for _, k := range "no" {
		m.Update(key(string(k)))
	}
	m.Update(key("enter"))
	if d := decisionOf(t, *out); d["behavior"] != "deny" || d["message"] != "no" {
		t.Fatalf("%v", d)
	}
}

func TestAnsweredInPaneClearsOnProgress(t *testing.T) {
	m, _, _ := hookModel(t)
	out := permission(m, 1, "r1")
	// the "needs permission" notification comes later and must not clear it
	m.Update(hookMsg(hook.Event{ID: 2, Agent: "r1", Tool: "claude", Event: "Notification", Payload: []byte(`{"notification_type":"permission_prompt"}`)}))
	if m.waitingCount() != 1 || *out != nil {
		t.Fatal("notification cleared the pending request")
	}
	m.Update(hookMsg(hook.Event{ID: 3, Agent: "r1", Tool: "claude", Event: "PostToolUse", Payload: []byte(`{}`)}))
	if m.waitingCount() != 0 || *out == nil || len(*out) != 0 {
		t.Fatalf("pending not released with an empty reply: %q", *out)
	}
	if !rowHas(view(m), "one", "working") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestClosedHookDropsPending(t *testing.T) {
	m, _, _ := hookModel(t)
	permission(m, 7, "r1")
	m.Update(hookMsg(hook.Event{ID: 99, Agent: "r1", Closed: true})) // some other request: ignored
	if m.waitingCount() != 1 {
		t.Fatal("unrelated close dropped the request")
	}
	m.Update(hookMsg(hook.Event{ID: 7, Agent: "r1", Closed: true}))
	if m.waitingCount() != 0 {
		t.Fatal("closed request still waiting")
	}
}

func TestApproveOldestWhenSelectionHasNone(t *testing.T) {
	m, _, _ := hookModel(t)
	first := permission(m, 1, "r2")
	permission(m, 2, "r1")
	m.cursorTo(t, "outside")
	m.Update(key("a"))
	if !strings.Contains(m.modal.title, "ABC-1") {
		t.Fatalf("modal %q", m.modal.title)
	}
	m.Update(key("y"))
	if *first == nil {
		t.Fatal("oldest request (r2) was not the one answered")
	}
}

func TestTrustPromptDetected(t *testing.T) {
	m, ft, notes := hookModel(t)
	ft.screen = "Quick safety check: Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder"
	msg := m.watchTrust("r1", "%1", claude.New())()
	_, cmd := m.Update(msg)
	ft.screen = "" // let the "prompt gone" watcher in cmd finish
	runCmd(cmd)
	if v := view(m); !strings.Contains(v, "answer the folder-trust prompt") || len(*notes) != 1 {
		t.Fatalf("view:\n%s\nnotes %v", v, *notes)
	}
	m.Update(hookMsg(hook.Event{ID: 1, Agent: "r1", Tool: "claude", Event: "UserPromptSubmit", Payload: []byte(`{}`)}))
	if strings.Contains(view(m), "folder-trust") {
		t.Fatal("trust note survived the first hook event")
	}
}

func TestTrustNoteClearsWhenPromptLeavesScreen(t *testing.T) {
	m, ft, _ := hookModel(t)
	ft.screen = "Yes, I trust this folder"
	m.Update(m.watchTrust("r1", "%1", claude.New())())
	ft.screen = "❯ " // answered; Claude sits idle, no hook fires
	m.Update(m.watchTrustGone("r1", "%1", claude.New())())
	if strings.Contains(view(m), "folder-trust") {
		t.Fatal("trust note stuck after the prompt was answered")
	}
}

func TestOpenDialogNeverAnswersAReplacementRequest(t *testing.T) {
	m, _, _ := hookModel(t)
	first := permission(m, 1, "r1")
	m.Update(key("a")) // dialog shows request 1
	// request 1 is answered in the pane; the agent moves on and asks again
	m.Update(hookMsg(hook.Event{ID: 2, Agent: "r1", Tool: "claude", Event: "PostToolUse", Payload: []byte(`{}`)}))
	if m.modal != nil {
		t.Fatal("dialog for an answered request stayed open")
	}
	second := permission(m, 3, "r1")
	m.Update(key("y")) // aimed at the old dialog; must not reach request 2
	if *second != nil {
		t.Fatalf("request 2 answered by a stale key press: %q", *second)
	}
	if first == nil || len(*first) != 0 {
		t.Fatalf("request 1 should have been released empty: %q", *first)
	}
}

func TestStaleDecisionAfterEditRefused(t *testing.T) {
	m, _, _ := hookModel(t)
	permission(m, 1, "r1")
	m.Update(key("a"))
	m.Update(key("m")) // message input for request 1
	m.Update(hookMsg(hook.Event{ID: 2, Agent: "r1", Tool: "claude", Event: "Stop", Payload: []byte(`{}`)}))
	second := permission(m, 3, "r1")
	if m.modal != nil {
		t.Fatal("message input for an answered request stayed open")
	}
	if *second != nil {
		t.Fatal("request 2 answered")
	}
}

func TestRefreshFromBeforeLaunchIgnored(t *testing.T) {
	m, _, _ := hookModel(t)
	m.agentSeq = 4 // a refresh (#4) is in flight
	m.launched(launchedMsg{rec: stateRecord("r9", "%9")})
	m.Update(agentsMsg{seq: 4, live: nil}) // finishes after the launch, without r9
	if !slicesContainsID(m.live, "r9") {
		t.Fatal("pre-launch refresh dropped the new agent")
	}
}

func stateRecord(id, pane string) state.Record {
	return state.Record{ID: id, Pane: pane, TicketKey: "ABC-1"}
}
func slicesContainsID(rs []state.Record, id string) bool {
	return slices.ContainsFunc(rs, func(r state.Record) bool { return r.ID == id })
}

func TestActivityLogKeepsLast20(t *testing.T) {
	m, _, _ := hookModel(t)
	for i := range 25 {
		m.Update(hookMsg(hook.Event{ID: i, Agent: "r1", Tool: "claude", Event: "PreToolUse",
			Payload: []byte(`{"tool_name":"Bash","tool_input":{"command":"step ` + string(rune('a'+i)) + `"}}`)}))
	}
	out := permission(m, 100, "r1")
	m.Update(key("a"))
	m.Update(key("y"))
	log := m.hooks["r1"].activity
	if len(log) != 20 || log[len(log)-1].text != "you allowed it" || log[len(log)-2].text != "asked: Bash: date > probe.txt" {
		t.Fatalf("log %+v (reply %q)", log, *out)
	}
}

func TestExternalSessionReportsThroughGlobalHooks(t *testing.T) {
	m, _, notes := hookModel(t)
	out := permission(m, 1, "claude:x9") // the "outside" session, id x9
	v := view(m)
	if !rowHas(v, "⚠ outside", "needs you") || !strings.Contains(v, "Bash: date > probe.txt") || len(*notes) != 1 {
		t.Fatalf("view:\n%s\nnotes %v", v, *notes)
	}
	m.Update(agentsMsg{agents: m.agents, live: m.live}) // a refresh must keep its state
	if m.waitingCount() != 1 {
		t.Fatal("refresh dropped the external session's request")
	}
	m.cursorTo(t, "outside")
	m.Update(key("a"))
	m.Update(key("y"))
	if d := decisionOf(t, *out); d["behavior"] != "allow" {
		t.Fatalf("%v", d)
	}
	// gone from the session list → state dropped
	m.Update(agentsMsg{agents: m.agents[:2], live: m.live})
	if _, ok := m.hooks["claude:x9"]; ok {
		t.Fatal("state kept for a session that's gone")
	}
}

// event sends a plain hook event for agent id and runs what it returns.
func event(m *Model, id, name string) {
	_, cmd := m.Update(hookMsg(hook.Event{Agent: id, Tool: "claude", Event: name, Payload: []byte(`{}`), Reply: func([]byte) {}}))
	if cmd == nil {
		return
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			if c != nil {
				c()
			}
		}
	}
}

func TestReadyWhenATurnFinishes(t *testing.T) {
	m, _, notes := hookModel(t)
	event(m, "r1", "UserPromptSubmit")
	event(m, "r1", "Stop")
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "ABC-1 is ready for you") {
		t.Fatalf("notes %v", *notes)
	}
	if v := view(m); !rowHas(v, "✓ one", "ready") || !strings.Contains(v, "✓ 1 ready") {
		t.Fatalf("view:\n%s", v)
	}
	m.cursorTo(t, "ABC-1 fix it")
	m.Update(key("j")) // onto "one": shown on the right, so you've seen it
	if strings.Contains(view(m), "✓ 1 ready") {
		t.Fatalf("still ready after looking:\n%s", view(m))
	}
	event(m, "r1", "UserPromptSubmit") // the shown agent finishing again: no ping
	event(m, "r1", "Stop")
	if len(*notes) != 1 {
		t.Fatalf("pinged for the agent on screen: %v", *notes)
	}
}

func TestTermNotice(t *testing.T) {
	if got := termNotice("osc9", "ABC-1 is ready"); got != "\x1bPtmux;\x1b\x1b]9;lanes: ABC-1 is ready\x07\x1b\\" {
		t.Fatalf("osc9 %q", got)
	}
	if got := termNotice("osc777", "a;b"); !strings.Contains(got, "]777;notify;lanes;ab\x07") {
		t.Fatalf("osc777 %q", got)
	}
	if got := termNotice("osc99", "x"); !strings.Contains(got, "]99;;lanes: x\x1b\x1b\\") {
		t.Fatalf("osc99 %q", got)
	}
	if got := termNotice("osc9", "evil\x07\x1b]0;title"); strings.Count(got, "\x07") != 1 {
		t.Fatalf("control characters got through: %q", got)
	}
	if termNotice("", "x") != "" {
		t.Fatal("no protocol, no sequence")
	}
}

func TestPingUsesTheTerminalOrTheSystemNotOneThenTheOther(t *testing.T) {
	m, _ := controlModel(t)
	var byTerm []bool
	m.opt.Notify = func(_ string, b bool) { byTerm = append(byTerm, b) }
	for _, proto := range []string{"osc9", ""} {
		m.opt.TermNotify = func() string { return proto }
		var got tea.Msg
		for _, c := range m.ping("ABC-1 is ready for you")().(tea.BatchMsg) {
			if msg := c(); msg != nil {
				got = msg
			}
		}
		if _, sent := got.(termNoticeMsg); sent != (proto != "") {
			t.Fatalf("%q: terminal notice %v", proto, got)
		}
	}
	if len(byTerm) != 2 || !byTerm[0] || byTerm[1] {
		t.Fatalf("system notifier told byTerminal=%v", byTerm)
	}
}

func TestPaneStatusChangesNotify(t *testing.T) {
	m, _ := controlModel(t) // its adapter has no hooks: status comes from the pane
	var notes []string
	m.opt.Notify = func(s string, _ bool) { notes = append(notes, s) }
	w, _ := ctl(m, 1, "wait", `{}`)
	set := func(s agent.Status) {
		agents := slices.Clone(m.agents)
		agents[0].Status = s
		_, cmd := m.Update(agentsMsg{seq: m.agentSeen + 1, agents: agents, live: m.live})
		runCmd(cmd)
	}
	set(agent.Working)
	set(agent.Idle) // a turn ended
	if len(notes) != 1 || !strings.Contains(notes[0], "ABC-1 is ready for you") || m.readyCount() != 1 {
		t.Fatalf("notes %v ready %d", notes, m.readyCount())
	}
	if evs, _ := (*w)["events"].([]any); len(evs) != 1 || evs[0].(map[string]any)["kind"] != "ready" {
		t.Fatalf("%v", *w)
	}
	set(agent.Waiting)
	if len(notes) != 2 || !strings.Contains(notes[1], "needs you") || m.readyCount() != 0 {
		t.Fatalf("notes %v ready %d", notes, m.readyCount())
	}
}
