package suggest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/agent/claude"
)

var open = map[string]bool{"ABC-1": true, "ABC-2": true, "XYZ-9": true}

// The fixtures are Claude transcripts, read through Claude's adapter.
var cl = claude.New()

// score ranks what a transcript (given as its text) mentions.
func score(t *testing.T, transcript string, open map[string]bool) Result {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(transcript), 0o600)
	return File(cl, p, open)
}

func line(typ, content string) string {
	return fmt.Sprintf(`{"type":%q,"message":{"content":%s}}`, typ, content)
}

func TestScoreWeightsAndFilters(t *testing.T) {
	transcript := strings.Join([]string{
		line("user", `"please look at ABC-2"`),
		line("assistant", `[{"type":"text","text":"ABC-2 and ABC-1 and CLOSED-7"},{"type":"tool_use","input":{"command":"git switch abc-1/fix"}}]`),
		line("user", `[{"type":"tool_result","content":"ABC-2 ABC-2 ABC-2 ABC-2"}]`), // output: ignored
		line("user", `[{"type":"text","text":"now ABC-1 please, abc-0001 too"}]`),
		`not json`,
		line("system", `"ABC-2"`),
	}, "\n")
	r := score(t, transcript, open)
	if len(r) != 2 || r[0].Key != "ABC-1" || r[0].Mentions != 4 || r[0].Typed != 2 || r[1].Key != "ABC-2" || r[1].Mentions != 2 || r[1].Typed != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestConfident(t *testing.T) {
	cases := []struct {
		r    Result
		want bool
	}{
		{nil, false},
		{Result{{Key: "A", Score: 50, Mentions: 4}}, false},
		{Result{{Key: "A", Score: 50, Mentions: 5}}, true},
		{Result{{Key: "A", Score: 50, Mentions: 9}, {Key: "B", Score: 25, Mentions: 9}}, true},
		{Result{{Key: "A", Score: 50, Mentions: 9}, {Key: "B", Score: 26, Mentions: 9}}, false},
		{Result{{Key: "A", Score: 50, Mentions: 9}, {Key: "B", Score: 20}, {Key: "C", Score: 20}, {Key: "D", Score: 20}}, false}, // a review of many tickets
	}
	for i, c := range cases {
		if c.r.Confident() != c.want {
			t.Errorf("case %d: %v", i, !c.want)
		}
	}
}

func TestRecencyBreaksTies(t *testing.T) {
	transcript := line("user", `"ABC-1"`) + "\n" + line("user", `"ABC-2"`)
	if r := score(t, transcript, open); r[0].Key != "ABC-2" {
		t.Fatalf("later mention should win: %+v", r)
	}
}

func TestFileReadsOnlyTheTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var b strings.Builder
	old := line("user", `"XYZ-9"`) + "\n"
	for b.Len() < tailBytes+len(old) {
		b.WriteString(old)
	}
	b.WriteString(line("user", `"ABC-1"`) + "\n")
	os.WriteFile(p, []byte(b.String()), 0o600)
	r := File(cl, p, open)
	if r[0].Key != "XYZ-9" || r[len(r)-1].Key != "ABC-1" || r[0].Mentions >= strings.Count(b.String(), "XYZ-9") {
		t.Fatalf("tail not applied: %+v", r)
	}
}

func TestInjectedTextIsNotTyped(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","isMeta":true,"message":{"content":"skill text about ABC-1 ABC-1 ABC-1"}}`,
		line("user", `"<task-notification>agent finished ABC-1</task-notification>"`),
		line("user", `[{"type":"text","text":"<system-reminder>ABC-1</system-reminder>"}]`),
		line("user", `"please do ABC-2"`),
	}, "\n")
	r := score(t, transcript, open)
	var a1, a2 Candidate
	for _, c := range r {
		switch c.Key {
		case "ABC-1":
			a1 = c
		case "ABC-2":
			a2 = c
		}
	}
	if a1.Typed != 0 || a1.Mentions != 5 || a2.Typed != 1 {
		t.Fatalf("ABC-1 %+v ABC-2 %+v", a1, a2)
	}
	if a1.Score >= 5*weightTyped { // 5 injected mentions must not outweigh like typed ones
		t.Fatalf("injected mentions weighted as typed: %+v", a1)
	}
}

func TestPromptsAndAnyKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(strings.Join([]string{
		line("user", `"first ask about ZZZ-5"`),
		`{"type":"user","isMeta":true,"message":{"content":"injected"}}`,
		line("user", `[{"type":"text","text":"second   ask"}]`),
		line("user", `"<system-reminder>no</system-reminder>"`),
		line("assistant", `"assistant text"`),
		line("user", `"third"`),
	}, "\n")), 0o600)
	if got := Prompts(cl, p, 2); len(got) != 2 || got[0] != "second ask" || got[1] != "third" {
		t.Fatalf("%q", got)
	}
	r := File(cl, p, nil)
	if len(r) != 1 || r[0].Key != "ZZZ-5" {
		t.Fatalf("any-key scoring: %+v", r)
	}
}

func TestLastReply(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(strings.Join([]string{
		line("user", `"first"`),
		line("assistant", `[{"type":"text","text":"old answer"}]`),
		line("user", `"second"`),
		line("assistant", `[{"type":"text","text":"looking"},{"type":"tool_use","input":{}}]`),
		line("user", `[{"type":"tool_result","content":"x"}]`),
		`{"type":"user","isMeta":true,"message":{"content":"injected"}}`,
		line("assistant", `[{"type":"text","text":"done: it works"}]`),
	}, "\n")), 0o600)
	if got := LastReply(cl, p); got != "looking\n\ndone: it works" {
		t.Fatalf("%q", got)
	}
}
