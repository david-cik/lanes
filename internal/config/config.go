// Package config loads lanes settings from TOML and resolves XDG paths.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Duration is a time.Duration that decodes from strings like "60s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	LinearPoll   Duration `toml:"linear_poll"`
	ExternalPoll Duration `toml:"external_poll"`
	Assignee     string   `toml:"assignee"`
}

func Default() Config {
	return Config{
		LinearPoll:   Duration{60 * time.Second},
		ExternalPoll: Duration{5 * time.Second},
		Assignee:     "me",
	}
}

// Path returns $XDG_CONFIG_HOME/lanes/config.toml (default ~/.config/lanes/config.toml).
func Path() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "lanes", "config.toml")
}

// Load reads the config file at path; a missing file yields defaults.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) && len(de.Key()) > 0 {
			return c, fmt.Errorf("%s: %v: %w", path, de.Key(), err)
		}
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// StateDir returns $XDG_STATE_HOME/lanes (default ~/.local/state/lanes), creating it 0700.
func StateDir() (string, error) {
	dir := filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "lanes")
	return dir, os.MkdirAll(dir, 0o700)
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}
