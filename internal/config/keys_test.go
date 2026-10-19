package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckKeys(t *testing.T) {
	for keys, want := range map[*map[string]string]string{
		{"start": "S"}:              "",
		{"nope": "z"}:               "unknown action",
		{"start": "SS"}:             "one character",
		{"start": "j"}:              "moves around the board",
		{"start": "s"}:              "are both",
		{"start": "s", "send": "n"}: "", // a swap is fine
	} {
		err := CheckKeys(*keys)
		if (want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want %q", *keys, err, want)
		}
	}
}

func TestWriteKeysKeepsTheRest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("# mine\nlaunch_dir = \"~/w\"\n\n[keys]\nstart = \"S\"\nhelp = \"h\"\n\n[agents.claude]\nargs = []\n"), 0o600)
	if err := WriteKeys(p, map[string]string{"start": "n", "send": "m", "ask": ";"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "# mine\nlaunch_dir = \"~/w\"\n\n[agents.claude]\nargs = []\n\n[keys] # set by `lanes keys`; `lanes keys --reset` removes it\nsend = \"m\"\nask = \";\"\n"
	if string(b) != want {
		t.Fatalf("got\n%s", b)
	}
	c, err := Load(p)
	if err != nil || c.Key("send") != "m" || c.Key("start") != "n" || c.Key("ask") != ";" {
		t.Fatalf("%v %v", err, c.Keys)
	}
	WriteKeys(p, nil) // reset
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "[keys]") || !strings.Contains(string(b), "[agents.claude]") {
		t.Fatalf("reset:\n%s", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestWriteKeysReplacesHandWrittenForms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("keys.start = \"S\"\nkeys = { send = \"m\" }\nnotify_os = true\n"), 0o600)
	if err := WriteKeys(p, map[string]string{"stop": "X"}); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(p); err != nil || c.Key("stop") != "X" || c.Key("start") != "n" || !c.NotifyOS {
		b, _ := os.ReadFile(p)
		t.Fatalf("%v\n%s", err, b)
	}
}

func TestWriteKeysWithNoConfigYet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none", "lanes", "config.toml")
	if err := WriteKeys(p, nil); err != nil { // reset with no config: nothing to do
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("made a file: %v", err)
	}
	if err := WriteKeys(p, map[string]string{"start": "S"}); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(p); err != nil || c.Key("start") != "S" {
		t.Fatalf("%v %v", err, c.Keys)
	}
}
