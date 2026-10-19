package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/david-cik/lanes/internal/agent"
)

// Codex keeps each session as <home>/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl, and
// session titles in <home>/session_index.jsonl. Codex says the format isn't stable, so
// reading is lenient: lines that don't look as expected are skipped.

func home() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

func rollouts(pattern string) []string {
	paths, _ := filepath.Glob(filepath.Join(home(), "sessions", "*", "*", "*", "rollout-*"+pattern+".jsonl"))
	return paths
}

// idOf is the session id at the end of a rollout's name (a UUID: 36 characters).
func idOf(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(name) < 36 {
		return ""
	}
	return name[len(name)-36:]
}

func (*Adapter) TranscriptPath(_, id string) string {
	if len(id) != 36 || strings.ContainsAny(id, "/*?[") {
		return ""
	}
	if p := rollouts(id); len(p) > 0 {
		return p[0]
	}
	return ""
}

func (*Adapter) Transcripts(since time.Time) map[string]string {
	out := map[string]string{}
	for _, p := range rollouts("") {
		if fi, err := os.Stat(p); err == nil && !fi.ModTime().Before(since) && idOf(p) != "" {
			out[idOf(p)] = p
		}
	}
	return out
}

type line struct {
	Type    string `json:"type"`
	Payload struct {
		Type       string `json:"type"`
		Cwd        string `json:"cwd"`
		Originator string `json:"originator"`
		Source     any    `json:"source"`
		Role       string `json:"role"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Arguments string `json:"arguments"` // function_call
		Input     string `json:"input"`     // custom_tool_call
	} `json:"payload"`
}

func messages(raw []byte) []agent.Msg {
	var l line
	if json.Unmarshal(raw, &l) != nil || l.Type != "response_item" {
		return nil
	}
	p := l.Payload
	switch {
	case p.Type == "message" && (p.Role == "user" || p.Role == "assistant"):
		var out []agent.Msg
		for _, c := range p.Content {
			if c.Text == "" {
				continue
			}
			user := p.Role == "user"
			// Codex puts its own context on the user's side in tags, e.g. <environment_context>.
			typed := user && !strings.HasPrefix(strings.TrimSpace(c.Text), "<")
			out = append(out, agent.Msg{User: user, Typed: typed, Text: c.Text})
		}
		return out
	case p.Type == "function_call":
		return []agent.Msg{{Tool: p.Arguments}}
	case p.Type == "custom_tool_call":
		return []agent.Msg{{Tool: p.Input}}
	}
	return nil
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
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		out = append(out, messages(sc.Bytes())...)
	}
	return out
}

// SessionInfo reads where the session ran and how it was started (its first line), its
// first prompt, and its title (the session index). `codex exec` runs are tools', not a
// person's.
func (*Adapter) SessionInfo(path string) (agent.Info, error) {
	var in agent.Info
	f, err := os.Open(path)
	if err != nil {
		return in, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		in.When = fi.ModTime()
	}
	sc := bufio.NewScanner(io.LimitReader(f, 4<<20))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() && (in.Cwd == "" || in.First == "") {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Type == "session_meta" {
			src, _ := l.Payload.Source.(string)
			in.Cwd, in.Entrypoint = l.Payload.Cwd, l.Payload.Originator
			in.Background = src == "exec" || strings.Contains(l.Payload.Originator, "exec")
			continue
		}
		for _, m := range messages(sc.Bytes()) {
			if s := oneLine(m.Text); m.Typed && s != "" && in.First == "" {
				in.First = s
			}
		}
	}
	in.Title = title(idOf(path))
	return in, nil
}

// title is a session's latest name in Codex's session index.
func title(id string) string {
	f, err := os.Open(filepath.Join(home(), "session_index.jsonl"))
	if err != nil || id == "" {
		return ""
	}
	defer f.Close()
	name := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), id) {
			continue
		}
		var e struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID == id && e.Name != "" {
			name = e.Name
		}
	}
	return name
}
