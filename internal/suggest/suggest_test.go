package suggest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var open = map[string]bool{"ABC-1": true, "ABC-2": true, "XYZ-9": true}

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
	r := Score(strings.NewReader(transcript), open)
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
	}
	for i, c := range cases {
		if c.r.Confident() != c.want {
			t.Errorf("case %d: %v", i, !c.want)
		}
	}
}

func TestRecencyBreaksTies(t *testing.T) {
	transcript := line("user", `"ABC-1"`) + "\n" + line("user", `"ABC-2"`)
	if r := Score(strings.NewReader(transcript), open); r[0].Key != "ABC-2" {
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
	r, err := File(p, open)
	if err != nil {
		t.Fatal(err)
	}
	if r[0].Key != "XYZ-9" || r[len(r)-1].Key != "ABC-1" || r[0].Mentions >= strings.Count(b.String(), "XYZ-9") {
		t.Fatalf("tail not applied: %+v", r)
	}
}

func TestTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	cwd := "/Users/me/src/my.app"
	want := filepath.Join(dir, "projects", "-Users-me-src-my-app", "s1.jsonl")
	os.MkdirAll(filepath.Dir(want), 0o700)
	os.WriteFile(want, nil, 0o600)
	if got := TranscriptPath(dir, cwd, "s1"); got != want {
		t.Fatalf("got %q", got)
	}
	other := filepath.Join(dir, "projects", "elsewhere", "s2.jsonl")
	os.MkdirAll(filepath.Dir(other), 0o700)
	os.WriteFile(other, nil, 0o600)
	if got := TranscriptPath(dir, cwd, "s2"); got != other {
		t.Fatalf("fallback got %q", got)
	}
	if got := TranscriptPath(dir, cwd, "nope"); got != "" {
		t.Fatalf("missing got %q", got)
	}
}

func TestInjectedTextIsNotTyped(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","isMeta":true,"message":{"content":"skill text about ABC-1 ABC-1 ABC-1"}}`,
		line("user", `"<task-notification>agent finished ABC-1</task-notification>"`),
		line("user", `[{"type":"text","text":"<system-reminder>ABC-1</system-reminder>"}]`),
		line("user", `"please do ABC-2"`),
	}, "\n")
	r := Score(strings.NewReader(transcript), open)
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

func TestSessionInfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(strings.Join([]string{
		`{"type":"mode","mode":"x"}`,
		`{"type":"user","cwd":"/src/app","message":{"content":"hi"}}`,
		`{"type":"ai-title","aiTitle":"Early title"}`,
		`{"type":"ai-title","aiTitle":"Fix login redirect"}`,
		`garbage`,
	}, "\n")), 0o600)
	in, err := SessionInfo(p)
	if err != nil || in.Cwd != "/src/app" || in.Title != "Fix login redirect" || in.When.IsZero() {
		t.Fatalf("%+v %v", in, err)
	}
	os.WriteFile(p, []byte(`{"cwd":"/x"}`+"\n"+`{"type":"ai-title","aiTitle":"ai"}`+"\n"+`{"type":"custom-title","customTitle":"mine"}`+"\n"), 0o600)
	if in, _ := SessionInfo(p); in.Title != "mine" {
		t.Fatalf("custom title should win: %+v", in)
	}
}
