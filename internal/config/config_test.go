package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c != Default() {
		t.Fatalf("got %+v, want defaults", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("linear_poll = \"2m\"\nassignee = \"someone@example.com\"\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.LinearPoll.Duration != 2*time.Minute || c.Assignee != "someone@example.com" {
		t.Fatalf("got %+v", c)
	}
	if c.ExternalPoll.Duration != 5*time.Second {
		t.Fatalf("unset field lost its default: %v", c.ExternalPoll)
	}
}

func TestLoadBadDurationNamesKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("external_poll = \"soon\"\n"), 0o600)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "external_poll") {
		t.Fatalf("want error naming external_poll, got %v", err)
	}
}

func TestStateDirHonorsXDG(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || dir != filepath.Join(base, "lanes") || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir=%s err=%v", dir, err)
	}
}

func TestRelativeXDGIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	if p := Path(); !filepath.IsAbs(p) {
		t.Fatalf("relative XDG_CONFIG_HOME used: %s", p)
	}
}

func TestPollFloor(t *testing.T) {
	for _, key := range []string{"linear_poll", "external_poll"} {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(key+" = \"0s\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: want floor error, got %v", key, err)
		}
	}
}
