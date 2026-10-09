package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/david-cik/lanes/internal/gh"
	"github.com/david-cik/lanes/internal/tracker"
)

type prTracker struct {
	*fakeTracker
	urls []string
}

func (p prTracker) Issue(_ context.Context, key string) (tracker.IssueDetail, error) {
	return tracker.IssueDetail{Issue: tracker.Issue{Key: key}, PRURLs: p.urls}, nil
}

func prModel(t *testing.T, urls ...string) (*Model, *[]string) {
	m, _ := controlModel(t)
	m.opt.Tracker = prTracker{&fakeTracker{}, urls}
	m.readers.PR = func(_ context.Context, _, u string) (*gh.PR, error) {
		n := map[string]int{"https://github.com/acme/app/pull/7": 7, "https://github.com/acme/api/pull/9": 9}[u]
		return &gh.PR{Number: n, Title: "change " + u[len(u)-1:], URL: u, State: "MERGED"}, nil
	}
	opened := &[]string{}
	m.readers.Open = func(u string) error { *opened = append(*opened, u); return nil }
	m.cursorTo(t, "ABC-1 fix it")
	return m, opened
}

func pressP(m *Model) {
	_, cmd := m.Update(key("p"))
	_, cmd = m.Update(cmd())
	if cmd != nil {
		m.Update(cmd())
	}
}

func TestPRsNone(t *testing.T) {
	m, opened := prModel(t)
	pressP(m)
	if len(*opened) != 0 || !strings.Contains(m.notice, "no pull requests are linked to ABC-1") {
		t.Fatalf("opened %v notice %q", *opened, m.notice)
	}
}

func TestPRsOneOpensIt(t *testing.T) {
	m, opened := prModel(t, "https://github.com/acme/app/pull/7")
	pressP(m)
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/acme/app/pull/7" {
		t.Fatalf("opened %v", *opened)
	}
}

func TestPRsSeveralListThem(t *testing.T) {
	m, opened := prModel(t, "https://github.com/acme/app/pull/7", "https://github.com/acme/api/pull/9")
	pressP(m)
	if m.modal == nil || len(m.modal.items) != 2 || !strings.Contains(m.modal.items[1].label, "#9 change 9") ||
		!strings.Contains(m.modal.items[1].label, "merged · acme/api") {
		t.Fatalf("list %+v", m.modal)
	}
	m.Update(key("down"))
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/acme/api/pull/9" {
		t.Fatalf("opened %v", *opened)
	}
}
