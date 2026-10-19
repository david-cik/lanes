package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/david-cik/lanes/internal/agent"
)

// Claude keeps a JSONL transcript per session under ~/.claude/projects/<folder slug>/.
// Its format isn't documented, so parsing is lenient: lines that don't look as expected
// are skipped, never fatal.

// ConfigDir is Claude's own folder ($CLAUDE_CONFIG_DIR, else ~/.claude).
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9]`)

func (*Adapter) TranscriptPath(cwd, id string) string {
	if id == "" || strings.ContainsAny(id, "/*?[") {
		return ""
	}
	p := filepath.Join(ConfigDir(), "projects", slugRe.ReplaceAllString(cwd, "-"), id+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if m, _ := filepath.Glob(filepath.Join(ConfigDir(), "projects", "*", id+".jsonl")); len(m) > 0 {
		return m[0]
	}
	return ""
}

func (*Adapter) Transcripts(since time.Time) map[string]string {
	paths, _ := filepath.Glob(filepath.Join(ConfigDir(), "projects", "*", "*.jsonl"))
	out := map[string]string{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.ModTime().Before(since) {
			out[strings.TrimSuffix(filepath.Base(p), ".jsonl")] = p
		}
	}
	return out
}

type entry struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"` // text Claude injected (skills, reminders), not typed
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type piece struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// injected recognises text Claude adds to the user's side of the conversation.
func injected(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"<system-reminder>", "<task-notification>", "<local-command", "<command-"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// messages turns one transcript line into what it says: one Msg per text, and per tool
// call. Tool results (command output) are left out: too noisy to mean anything.
func messages(line []byte) []agent.Msg {
	var e entry
	if json.Unmarshal(line, &e) != nil || (e.Type != "user" && e.Type != "assistant") {
		return nil
	}
	user := e.Type == "user"
	msg := func(text string) agent.Msg {
		return agent.Msg{User: user, Typed: user && !e.IsMeta && !injected(text), Text: text}
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return []agent.Msg{msg(s)}
	}
	var parts []piece
	if json.Unmarshal(e.Message.Content, &parts) != nil {
		return nil
	}
	var out []agent.Msg
	for _, p := range parts {
		switch {
		case p.Type == "text":
			out = append(out, msg(p.Text))
		case p.Type == "tool_use" && !user:
			out = append(out, agent.Msg{Tool: string(p.Input)})
		}
	}
	return out
}

func (*Adapter) Messages(path string, tail int64) []agent.Msg {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var r io.Reader = f
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		if _, err := f.Seek(-tail, io.SeekEnd); err != nil {
			return nil
		}
		br := bufio.NewReader(f)
		br.ReadString('\n') // drop the partial first line
		r = br
	}
	var out []agent.Msg
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		out = append(out, messages(sc.Bytes())...)
	}
	return out
}

// SessionInfo reads a transcript's folder and first prompt (from its start) and latest
// title (from its end).
func (*Adapter) SessionInfo(path string) (agent.Info, error) {
	var in agent.Info
	f, err := os.Open(path)
	if err != nil {
		return in, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return in, err
	}
	in.When = fi.ModTime()
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && (in.Cwd == "" || in.First == "") {
		var head struct {
			Cwd        string `json:"cwd"`
			Entrypoint string `json:"entrypoint"`
		}
		if json.Unmarshal(sc.Bytes(), &head) == nil && in.Cwd == "" {
			in.Cwd, in.Entrypoint = head.Cwd, head.Entrypoint
			// Sessions started by tools and plugins (Agent SDK, `claude -p`).
			in.Background = strings.HasPrefix(head.Entrypoint, "sdk")
		}
		if in.First == "" {
			for _, m := range messages(sc.Bytes()) {
				if t := oneLine(m.Text); m.Typed && t != "" {
					in.First = t
					break
				}
			}
		}
	}
	const tail = 512 << 10
	if fi.Size() > tail {
		f.Seek(-tail, io.SeekEnd)
	} else {
		f.Seek(0, io.SeekStart)
	}
	var t struct {
		Type   string `json:"type"`
		Custom string `json:"customTitle"`
		AI     string `json:"aiTitle"`
	}
	custom := ""
	sc = bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		t.Custom, t.AI = "", ""
		if json.Unmarshal(sc.Bytes(), &t) != nil {
			continue
		}
		switch {
		case t.Type == "custom-title" && t.Custom != "":
			custom = t.Custom
		case t.Type == "ai-title" && t.AI != "":
			in.Title = t.AI
		}
	}
	if custom != "" {
		in.Title = custom
	}
	return in, nil
}
