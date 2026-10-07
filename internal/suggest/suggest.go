// Package suggest guesses which ticket a Claude session is working on by reading the
// transcript Claude keeps for it. The transcript format isn't documented, so parsing is
// lenient: lines that don't look as expected are skipped, never fatal.
package suggest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Only the end of a transcript is read: recent work says most about what the session
// is on now, and transcripts grow to tens of MB.
const tailBytes = 4 << 20

// Weights by where a key appears: typed by the user > in a command or file the agent
// used > in the agent's own prose.
const (
	weightTyped = 5.0
	weightTool  = 2.0
	weightText  = 1.0
)

var keyRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9])([A-Za-z][A-Za-z0-9]*)-0*(\d+)`)

type Candidate struct {
	Key      string
	Score    float64
	Mentions int
	Typed    int // mentions in the user's own messages
}

// Result ranks the open tickets a transcript mentions, best first.
type Result []Candidate

// Confident reports whether the best candidate clearly dominates: at least 5 mentions
// and at least twice the score of the runner-up.
func (r Result) Confident() bool {
	if len(r) == 0 || r[0].Mentions < 5 {
		return false
	}
	return len(r) == 1 || r[0].Score >= 2*r[1].Score
}

// TranscriptPath finds Claude's transcript for a session in dir, or "".
func TranscriptPath(configDir, cwd, sessionID string) string {
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".claude")
	}
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(cwd, "-")
	p := filepath.Join(configDir, "projects", slug, sessionID+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if m, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl")); len(m) > 0 {
		return m[0]
	}
	return ""
}

// File scores the end of the transcript at path against the given open ticket keys.
func File(path string, open map[string]bool) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		if _, err := f.Seek(-tailBytes, io.SeekEnd); err != nil {
			return nil, err
		}
		br := bufio.NewReader(f)
		br.ReadString('\n') // drop the partial first line
		r = br
	}
	return Score(r, open), nil
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

// Score reads JSONL transcript lines and ranks the open ticket keys they mention.
// Later lines weigh more (×0.5 at the start of what's read, ×1.5 at the end).
func Score(r io.Reader, open map[string]bool) Result {
	type hit struct {
		key    string
		weight float64
		typed  bool
	}
	var hits []hit
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || (e.Type != "user" && e.Type != "assistant") {
			continue
		}
		add := func(s string, w float64, typed bool) {
			if typed && (e.IsMeta || injected(s)) { // shown as user text, but not from the user
				w, typed = weightText, false
			}
			for _, m := range keyRe.FindAllStringSubmatch(s, -1) {
				k := strings.ToUpper(m[1]) + "-" + m[2]
				if open[k] {
					hits = append(hits, hit{k, w, typed})
				}
			}
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil { // plain string content
			if e.Type == "user" {
				add(s, weightTyped, true)
			} else {
				add(s, weightText, false)
			}
			continue
		}
		var parts []piece
		if json.Unmarshal(e.Message.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			switch {
			case p.Type == "text" && e.Type == "user":
				add(p.Text, weightTyped, true)
			case p.Type == "text":
				add(p.Text, weightText, false)
			case p.Type == "tool_use":
				add(string(p.Input), weightTool, false)
			} // tool results (command output) are skipped: too noisy
		}
	}
	byKey := map[string]*Candidate{}
	for i, h := range hits {
		recency := 0.5 + float64(i+1)/float64(len(hits))
		c := byKey[h.key]
		if c == nil {
			c = &Candidate{Key: h.key}
			byKey[h.key] = c
		}
		c.Score += h.weight * recency
		c.Mentions++
		if h.typed {
			c.Typed++
		}
	}
	var out Result
	for _, c := range byKey {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// Info is what a transcript says about its session.
type Info struct {
	Cwd   string
	Title string // latest custom or AI-generated title
	When  time.Time
}

// SessionInfo reads a transcript's folder (from its start) and latest title (from its
// end). Like the rest of this package it tolerates lines it doesn't understand.
func SessionInfo(path string) (Info, error) {
	var in Info
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
	var head struct {
		Cwd string `json:"cwd"`
	}
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && in.Cwd == "" {
		if json.Unmarshal(sc.Bytes(), &head) == nil {
			in.Cwd = head.Cwd
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
