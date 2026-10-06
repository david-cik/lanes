// Package install adds or removes lanes' global hooks in a Claude Code settings file,
// changing nothing else. The result is written in the same 2-space style Claude uses.
package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"

	"github.com/david-cik/lanes/internal/agent/claude"
)

var style = &pretty.Options{Indent: "  ", Width: 1} // one value per line, keys in file order

// Add returns settings with each entry appended to hooks.<Event>, skipping events that
// already have a lanes global hook. added lists the events changed.
func Add(settings []byte, entries []claude.HookEntry) (out []byte, added []string, err error) {
	doc := string(settings)
	if strings.TrimSpace(doc) == "" {
		doc = "{}"
	}
	if !gjson.Valid(doc) {
		return nil, nil, errors.New("settings file is not valid JSON")
	}
	for _, e := range entries {
		if hasGlobal(gjson.Get(doc, "hooks."+e.Event)) {
			continue
		}
		if doc, err = sjson.SetRaw(doc, "hooks."+e.Event+".-1", string(e.JSON)); err != nil {
			return nil, nil, err
		}
		added = append(added, e.Event)
	}
	return pretty.PrettyOptions([]byte(doc), style), added, nil
}

// Remove returns settings without lanes' global hook entries; event arrays and the
// hooks object are dropped if that leaves them empty.
func Remove(settings []byte) (out []byte, removed int, err error) {
	doc := string(settings)
	if !gjson.Valid(doc) {
		return nil, 0, errors.New("settings file is not valid JSON")
	}
	var events []string
	gjson.Get(doc, "hooks").ForEach(func(k, _ gjson.Result) bool {
		events = append(events, k.String())
		return true
	})
	for _, ev := range events {
		arr := gjson.Get(doc, "hooks."+ev).Array()
		for i := len(arr) - 1; i >= 0; i-- {
			if ours(arr[i]) {
				if doc, err = sjson.Delete(doc, fmt.Sprintf("hooks.%s.%d", ev, i)); err != nil {
					return nil, 0, err
				}
				removed++
			}
		}
		if len(gjson.Get(doc, "hooks."+ev).Array()) == 0 && len(arr) > 0 {
			doc, _ = sjson.Delete(doc, "hooks."+ev)
		}
	}
	if h := gjson.Get(doc, "hooks"); h.Exists() && len(h.Map()) == 0 && removed > 0 {
		doc, _ = sjson.Delete(doc, "hooks")
	}
	return pretty.PrettyOptions([]byte(doc), style), removed, nil
}

// ours: an entry whose every hook command is a lanes global hook.
func ours(entry gjson.Result) bool {
	hooks := entry.Get("hooks").Array()
	if len(hooks) == 0 {
		return false
	}
	for _, h := range hooks {
		if !strings.Contains(h.Get("command").String(), claude.GlobalMarker) {
			return false
		}
	}
	return true
}

func hasGlobal(eventHooks gjson.Result) bool {
	for _, e := range eventHooks.Array() {
		if ours(e) {
			return true
		}
	}
	return false
}

// Run installs (or with uninstall, removes) lanes' global hooks in the settings file at
// path, after showing what changes and asking unless yes. It keeps a timestamped backup.
func Run(path, bin string, uninstall, yes bool, in io.Reader, out io.Writer) error {
	// Edit the real file behind a symlink (dotfile managers), keeping the link intact.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var after []byte
	if uninstall {
		var n int
		if len(strings.TrimSpace(string(before))) == 0 {
			fmt.Fprintf(out, "No lanes hooks in %s; nothing to do.\n", path)
			return nil
		}
		if after, n, err = Remove(before); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if n == 0 {
			fmt.Fprintf(out, "No lanes hooks in %s; nothing to do.\n", path)
			return nil
		}
		fmt.Fprintf(out, "Will remove %d lanes hook entries from %s.\n", n, path)
	} else {
		var added []string
		if after, added, err = Add(before, claude.HookEntries(bin, true)); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(added) == 0 {
			fmt.Fprintf(out, "lanes hooks are already in %s; nothing to do.\n", path)
			return nil
		}
		fmt.Fprintf(out, "Will add a lanes hook to %s for: %s\n", path, strings.Join(added, ", "))
		fmt.Fprintf(out, "Each runs: '%s' hook --global claude <event>\n", bin)
		fmt.Fprintln(out, "They only report to a running lanes panel and never change what Claude does unless you answer a request in lanes.")
		fmt.Fprintln(out, "The file is rewritten in Claude's own format (2-space JSON, same key order).")
	}
	if !yes {
		fmt.Fprint(out, "Proceed? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Nothing changed.")
			return nil
		}
	}
	if before != nil {
		backup := fmt.Sprintf("%s.lanes-backup-%s", path, time.Now().Format("20060102-150405.000000000"))
		if err := os.WriteFile(backup, before, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "Backup: %s\n", backup)
	}
	if err := writeKeepingMode(path, after); err != nil {
		return err
	}
	fmt.Fprintln(out, "Done.")
	return nil
}

func writeKeepingMode(path string, b []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
