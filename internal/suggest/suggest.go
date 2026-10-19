// Package suggest guesses which ticket an agent session is working on from its
// transcript, read through its tool's adapter (agent.Transcripts).
package suggest

import (
	"regexp"
	"slices"
	"strings"

	"github.com/david-cik/lanes/internal/agent"
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
	total := 0.0
	for _, c := range r {
		total += c.Score
	}
	// Twice the runner-up, and at least half of everything: a session that went over
	// many tickets (a review, a planning pass) isn't about the one it named most.
	return (len(r) == 1 || r[0].Score >= 2*r[1].Score) && 2*r[0].Score >= total
}

// File ranks the open tickets the end of the transcript at path mentions (any plausible
// key when open is nil).
func File(t agent.Transcripts, path string, open map[string]bool) Result {
	return Score(t.Messages(path, tailBytes), open)
}

// Score ranks the open ticket keys msgs mention (any key when open is nil). Later
// messages weigh more (×0.5 at the start, ×1.5 at the end).
func Score(msgs []agent.Msg, open map[string]bool) Result {
	type hit struct {
		key    string
		weight float64
		typed  bool
	}
	var hits []hit
	add := func(s string, w float64, typed bool) {
		for _, m := range keyRe.FindAllStringSubmatch(s, -1) {
			k := strings.ToUpper(m[1]) + "-" + m[2]
			if open == nil && plausibleKey(m[1]) || open[k] { // nil: any plausible key
				hits = append(hits, hit{k, w, typed})
			}
		}
	}
	for _, m := range msgs {
		switch {
		case m.Typed:
			add(m.Text, weightTyped, true)
		case m.Text != "": // the agent's prose, or text the tool put on the user's side
			add(m.Text, weightText, false)
		default:
			add(m.Tool, weightTool, false)
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

// plausibleKey filters "any key" scoring to prefixes that look like a tracker's team
// key (2–5 letters), so file paths, terminal names, and ids don't count.
func plausibleKey(prefix string) bool {
	if len(prefix) < 2 || len(prefix) > 5 {
		return false
	}
	for _, r := range prefix {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// FileKeys scores a transcript against any key with one of the given prefixes
// (upper case, e.g. "SRE"); with no prefixes, any plausible-looking key.
func FileKeys(t agent.Transcripts, path string, prefixes map[string]bool) Result {
	r := File(t, path, nil)
	if len(prefixes) == 0 {
		return r
	}
	var out Result
	for _, c := range r {
		if p, _, ok := strings.Cut(c.Key, "-"); ok && prefixes[p] {
			out = append(out, c)
		}
	}
	return out
}

// Prompts returns the last n messages the user typed in the session, newest last, each
// squeezed to one line.
func Prompts(t agent.Transcripts, path string, n int) []string {
	var out []string
	for _, m := range t.Messages(path, 1<<20) {
		if s := oneLine(m.Text); m.Typed && s != "" {
			out = append(out, s)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// LastReply is what the assistant wrote since the last prompt typed into the session
// (its text, not its tool calls).
func LastReply(t agent.Transcripts, path string) string {
	var out []string
	for _, m := range t.Messages(path, 4<<20) {
		switch {
		case m.Typed && strings.TrimSpace(m.Text) != "":
			out = out[:0]
		case !m.User && strings.TrimSpace(m.Text) != "":
			out = append(out, strings.TrimSpace(m.Text))
		}
	}
	return strings.Join(out, "\n\n")
}

// oneLine squeezes text to one line of at most 200 characters.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}
