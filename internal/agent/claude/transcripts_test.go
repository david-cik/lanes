package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	cwd := "/Users/me/src/my.app"
	want := filepath.Join(dir, "projects", "-Users-me-src-my-app", "s1.jsonl")
	os.MkdirAll(filepath.Dir(want), 0o700)
	os.WriteFile(want, nil, 0o600)
	if got := (&Adapter{}).TranscriptPath(cwd, "s1"); got != want {
		t.Fatalf("got %q", got)
	}
	other := filepath.Join(dir, "projects", "elsewhere", "s2.jsonl")
	os.MkdirAll(filepath.Dir(other), 0o700)
	os.WriteFile(other, nil, 0o600)
	if got := (&Adapter{}).TranscriptPath(cwd, "s2"); got != other {
		t.Fatalf("fallback got %q", got)
	}
	if got := (&Adapter{}).TranscriptPath(cwd, "nope"); got != "" {
		t.Fatalf("missing got %q", got)
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
	in, err := (&Adapter{}).SessionInfo(p)
	if err != nil || in.Cwd != "/src/app" || in.Title != "Fix login redirect" || in.When.IsZero() || in.First != "hi" {
		t.Fatalf("%+v %v", in, err)
	}
	os.WriteFile(p, []byte(`{"cwd":"/x"}`+"\n"+`{"type":"ai-title","aiTitle":"ai"}`+"\n"+`{"type":"custom-title","customTitle":"mine"}`+"\n"), 0o600)
	if in, _ := (&Adapter{}).SessionInfo(p); in.Title != "mine" {
		t.Fatalf("custom title should win: %+v", in)
	}
}
