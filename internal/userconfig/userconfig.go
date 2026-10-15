// Package userconfig reads ~/.config/cartographer/config.toml: which
// workspace repositories to pull snapshots from, and where checkouts live.
package userconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Workspaces are GitHub repositories (owner/name) publishing snapshots.
	Workspaces []string `toml:"workspaces"`
	// Checkouts maps repository names to local paths when they aren't
	// siblings of the current repository.
	Checkouts map[string]string `toml:"checkouts"`
}

// Path is $CARTOGRAPHER_CONFIG, else $XDG_CONFIG_HOME/cartographer/config.toml
// or ~/.config/cartographer/config.toml.
func Path() string {
	if p := os.Getenv("CARTOGRAPHER_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cartographer", "config.toml")
}

// Load returns the config, or an empty one when the file doesn't exist.
func Load() (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(Path(), &c); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("userconfig: %w", err)
	}
	return &c, nil
}

var source = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// AddWorkspace records a workspace source (owner/name) and saves.
func AddWorkspace(src string) error {
	if !source.MatchString(src) {
		return fmt.Errorf("workspace source must be owner/name, got %q", src)
	}
	c, err := Load()
	if err != nil {
		return err
	}
	for _, w := range c.Workspaces {
		if w == src {
			return nil
		}
	}
	c.Workspaces = append(c.Workspaces, src)
	sort.Strings(c.Workspaces)
	return c.save()
}

func (c *Config) save() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(Path(), buf.Bytes(), 0o644)
}
