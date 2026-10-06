package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/david-cik/lanes/internal/agent/claude"
)

// Claude writes settings.json as JSON.stringify(v, null, 2) + newline.
const userSettings = `{
  "model": "opus",
  "permissions": {
    "allow": [
      "Bash(ls)"
    ]
  },
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "my-own-hook"
          }
        ]
      }
    ]
  },
  "statusLine": {}
}
`

func entries() []claude.HookEntry { return claude.HookEntries("/opt/lanes", true) }

func TestAddKeepsUserContentAndOrder(t *testing.T) {
	out, added, err := Add([]byte(userSettings), entries())
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 7 {
		t.Fatalf("added %v", added)
	}
	s := string(out)
	if gjson.Get(s, "hooks.Stop.0.hooks.0.command").String() != "my-own-hook" ||
		!strings.Contains(gjson.Get(s, "hooks.Stop.1.hooks.0.command").String(), claude.GlobalMarker) ||
		gjson.Get(s, "permissions.allow.0").String() != "Bash(ls)" {
		t.Fatalf("content changed:\n%s", s)
	}
	if strings.Index(s, `"model"`) > strings.Index(s, `"permissions"`) || strings.Index(s, `"hooks"`) > strings.Index(s, `"statusLine"`) {
		t.Fatalf("key order changed:\n%s", s)
	}
	again, added2, _ := Add(out, entries())
	if len(added2) != 0 || !bytes.Equal(again, out) {
		t.Fatal("second install was not a no-op")
	}
}

func TestRemoveRestoresOriginalByteForByte(t *testing.T) {
	for name, orig := range map[string]string{"user": userSettings, "empty": "{}\n", "no hooks": "{\n  \"model\": \"x\"\n}\n"} {
		added, _, _ := Add([]byte(orig), entries())
		back, n, err := Remove(added)
		if err != nil || n != 7 {
			t.Fatalf("%s: n=%d err=%v", name, n, err)
		}
		if string(back) != orig {
			t.Errorf("%s: not restored:\n%s\nwant:\n%s", name, back, orig)
		}
	}
}

func TestRemoveLeavesMixedEntries(t *testing.T) {
	mixed := `{"hooks":{"Stop":[{"hooks":[{"command":"'x' hook --global claude Stop"},{"command":"mine"}]}]}}`
	_, n, _ := Remove([]byte(mixed))
	if n != 0 {
		t.Fatal("removed an entry that also runs the user's command")
	}
}

func TestRunConsentBackupAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(userSettings), 0o644)
	var out bytes.Buffer
	if err := Run(path, "/opt/lanes", false, false, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != userSettings || !strings.Contains(out.String(), "Nothing changed") {
		t.Fatal("changed without consent")
	}
	if err := Run(path, "/opt/lanes", false, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	backups, _ := filepath.Glob(path + ".lanes-backup-*")
	if !strings.Contains(string(b), claude.GlobalMarker) || fi.Mode().Perm() != 0o644 || len(backups) != 1 {
		t.Fatalf("install: mode %v backups %v", fi.Mode().Perm(), backups)
	}
	if err := Run(path, "/opt/lanes", true, true, nil, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != userSettings {
		t.Fatalf("uninstall did not restore:\n%s", b)
	}
}

func TestInvalidJSONRefused(t *testing.T) {
	if _, _, err := Add([]byte("{nope"), entries()); err == nil {
		t.Fatal("accepted invalid JSON")
	}
}
