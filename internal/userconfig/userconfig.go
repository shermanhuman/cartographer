// Package userconfig reads ~/.config/cartographer/config.toml: which
// workspace repositories to pull snapshots from, which local workspaces to
// rebuild from, and where checkouts live.
package userconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Workspaces are GitHub repositories (owner/name) publishing snapshots.
	Workspaces []string `toml:"workspaces"`
	// LocalWorkspaces are directories holding a cartographer.toml, rebuilt
	// from local checkouts when they are ahead of the snapshot (in addition
	// to workspaces found beside the current repository).
	LocalWorkspaces []string `toml:"local_workspaces"`
	// AutoRebuild turns local rebuilds off when false; unset means on.
	AutoRebuild *bool `toml:"auto_rebuild"`
	// Checkouts maps repository names to local paths when they aren't
	// siblings of the current repository.
	Checkouts map[string]string `toml:"checkouts"`
}

// AutoRebuildOn reports whether local rebuilds are enabled (the default).
func (c *Config) AutoRebuildOn() bool {
	return c.AutoRebuild == nil || *c.AutoRebuild
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

// AddWorkspace records a workspace source (owner/name). It edits the file
// in place, so comments and other settings are kept.
func AddWorkspace(src string) error {
	if !source.MatchString(src) {
		return fmt.Errorf("workspace source must be owner/name, got %q", src)
	}
	return addItem("workspaces", src, func(c *Config) []string { return c.Workspaces })
}

// AddLocalWorkspace records the directory of a local workspace (absolute).
func AddLocalWorkspace(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("local workspace must be an absolute path, got %q", dir)
	}
	return addItem("local_workspaces", filepath.Clean(dir), func(c *Config) []string { return c.LocalWorkspaces })
}

// addItem appends item to the top-level array key, editing the file in
// place; get reads the array back from a decoded config.
func addItem(key, item string, get func(*Config) []string) error {
	c, err := Load()
	if err != nil {
		return err
	}
	if slices.Contains(get(c), item) {
		return nil
	}
	old, err := os.ReadFile(Path())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := withItem(string(old), key, item)
	var check Config
	if _, err := toml.Decode(next, &check); err != nil || !slices.Contains(get(&check), item) {
		return fmt.Errorf("userconfig: could not add %s to %s; add it to `%s` by hand", item, Path(), key)
	}
	return write([]byte(next))
}

// withItem returns body with src appended to its top-level key array, or
// with the array added before the first table when there is none.
func withItem(body, key, src string) string {
	item := strconv.Quote(src)
	arrayKey := regexp.MustCompile(`(?m)^[ \t]*` + key + `[ \t]*=[ \t]*\[`)
	if loc := arrayKey.FindStringIndex(body); loc != nil && loc[0] < firstTable(body) {
		if end, last := arrayEnd(body, loc[1]); end >= 0 {
			if last >= 0 && strings.Contains(body[last:end], "\n") {
				// Multi-line: the entry goes on its own line before the
				// bracket, indented like the last one, so comments stay put.
				lineStart := strings.LastIndex(body[:last], "\n") + 1
				indent := body[lineStart : lineStart+len(body[lineStart:])-len(strings.TrimLeft(body[lineStart:], " \t"))]
				comma := ","
				if body[last] == ',' || body[last] == '[' {
					comma = ""
				}
				bracketLine := strings.LastIndex(body[:end], "\n") + 1
				return body[:last+1] + comma + body[last+1:bracketLine] + indent + item + ",\n" + body[bracketLine:]
			}
			sep := ", "
			if last < 0 || body[last] == ',' || body[last] == '[' {
				sep = " "
			}
			if last < 0 {
				last = loc[1] - 1
			}
			return body[:last+1] + sep + item + body[last+1:]
		}
	}
	return key + " = [" + item + "]\n" + body
}

// firstTable is the offset of the first [table] header (len(body) if none).
func firstTable(body string) int {
	off := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			return off
		}
		off += len(line)
	}
	return len(body)
}

// arrayEnd scans an array whose contents start at i, skipping strings and
// comments. It returns the offset of the closing bracket and of the last
// significant character before it (-1 when the array is empty), or -1, -1
// when the array is not closed.
func arrayEnd(body string, i int) (end, last int) {
	last = -1
	for ; i < len(body); i++ {
		switch ch := body[i]; ch {
		case ']':
			return i, last
		case '#':
			for i < len(body) && body[i] != '\n' {
				i++
			}
		case '"', '\'':
			for i++; i < len(body) && body[i] != ch; i++ {
				if ch == '"' && body[i] == '\\' {
					i++
				}
			}
			last = i
		case ' ', '\t', '\r', '\n':
		default:
			last = i
		}
	}
	return -1, -1
}

// write replaces the config file atomically.
func write(b []byte) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
