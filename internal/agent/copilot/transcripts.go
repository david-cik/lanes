package copilot

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

// Copilot keeps each session in <home>/session-state/<id>/: events.jsonl (one event per
// line: session.start, user.message, assistant.message, …) and workspace.yaml (its name).

func home() string {
	if d := os.Getenv("COPILOT_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".copilot")
}

func (*Adapter) TranscriptPath(_, id string) string {
	if id == "" || strings.ContainsAny(id, "/*?[") {
		return ""
	}
	p := filepath.Join(home(), "session-state", id, "events.jsonl")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func (*Adapter) Transcripts(since time.Time) map[string]string {
	paths, _ := filepath.Glob(filepath.Join(home(), "session-state", "*", "events.jsonl"))
	out := map[string]string{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.ModTime().Before(since) {
			out[filepath.Base(filepath.Dir(p))] = p
		}
	}
	return out
}

type event struct {
	Type string `json:"type"`
	Data struct {
		Context struct {
			Cwd string `json:"cwd"`
		} `json:"context"`
		Content  string `json:"content"`
		Role     string `json:"role"`
		Requests []struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		} `json:"toolRequests"`
	} `json:"data"`
}

func messages(line []byte) []agent.Msg {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return nil
	}
	switch e.Type {
	case "user.message":
		return []agent.Msg{{User: true, Typed: true, Text: e.Data.Content}}
	case "assistant.message":
		var out []agent.Msg
		if e.Data.Content != "" {
			out = append(out, agent.Msg{Text: e.Data.Content})
		}
		for _, r := range e.Data.Requests {
			out = append(out, agent.Msg{Tool: string(r.Args)})
		}
		return out
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
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		out = append(out, messages(sc.Bytes())...)
	}
	return out
}

// SessionInfo reads where the session ran and what it was first asked (from the start
// of events.jsonl) and its name (workspace.yaml). A session run with -p (by a tool,
// lanes' own questions among them) says so in its system message.
func (*Adapter) SessionInfo(path string) (agent.Info, error) {
	in := agent.Info{Entrypoint: "copilot"}
	f, err := os.Open(path)
	if err != nil {
		return in, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		in.When = fi.ModTime()
	}
	sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
scan:
	for sc.Scan() { // up to the first turn: the start, the system message, the first prompt
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "assistant.turn_start":
			if in.First != "" {
				break scan
			}
		case "session.start":
			in.Cwd = e.Data.Context.Cwd
		case "system.message":
			in.Background = in.Background || strings.Contains(e.Data.Content, "running in non-interactive mode")
		case "user.message":
			if in.First == "" {
				in.First = oneLine(e.Data.Content)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "workspace.yaml")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "name: "); ok {
				in.Title = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return in, nil
}
