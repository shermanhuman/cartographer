// Package workspace reads cartographer.toml and builds snapshots from the
// repositories it lists.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// FileName is the workspace config file name.
const FileName = "cartographer.toml"

type Config struct {
	Name string `toml:"name"`
	// Publish is the GitHub repository (owner/name) whose releases hold snapshots.
	Publish string `toml:"publish"`
	// Keep is how many complete snapshot releases publish retains (default 30).
	Keep        int               `toml:"keep"`
	Contexts    Contexts          `toml:"contexts"`
	Annotations string            `toml:"annotations"` // dir, default "annotations"
	Repos       []Repo            `toml:"repos"`
	Plugins     map[string]Plugin `toml:"plugins"`
	Schema      *Schema           `toml:"schema"`

	dir string // directory containing the config
}

type Contexts struct {
	Depth int `toml:"depth"`
}

type Repo struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
	// Path is the local checkout, relative to the config file.
	Path string `toml:"path"`
	// Ref is the revision to snapshot (default HEAD).
	Ref     string   `toml:"ref"`
	Plugins []string `toml:"plugins"`
	// Docs are glob patterns for Markdown to include (default every tracked *.md).
	// "docs/" includes everything under docs/; "docs/*.md" only its top
	// level; ** isn't recursive (it acts like *).
	Docs []string `toml:"docs"`
}

// Plugin is an extractor command. Placeholders: {repo} {root} {revision}
// {plugins} {schema_facts} {workspace}.
type Plugin struct {
	Command []string          `toml:"command"`
	Version string            `toml:"version"`
	Env     map[string]string `toml:"env"`
}

// Schema configures database schema extraction. Prepare runs in the archived
// PrepareRepo (e.g. migrations against a disposable database), then Command
// emits a schema facts document. Both are skipped when the cache key is
// unchanged. The key covers Version, Command, Prepare, PrepareRepo, Env, the
// plugin code digest and, at the PrepareRepo revision, the files under the
// CacheKey paths (each must match something) or, with no CacheKey, the
// revision itself.
type Schema struct {
	PrepareRepo string            `toml:"prepare_repo"`
	Prepare     string            `toml:"prepare"`
	Command     []string          `toml:"command"`
	Version     string            `toml:"version"`
	CacheKey    []string          `toml:"cache_key"`
	Env         map[string]string `toml:"env"`
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Load reads and validates a config file (or the cartographer.toml in a dir).
func Load(path string) (*Config, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, FileName)
	}
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("workspace: %s: unknown keys %v", path, und)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)
	if !md.IsDefined("keep") {
		c.Keep = 30
	} else if c.Keep < 1 {
		return nil, fmt.Errorf("workspace: %s: keep must be at least 1, got %d", path, c.Keep)
	}
	if c.Contexts.Depth < 0 {
		return nil, fmt.Errorf("workspace: %s: contexts.depth must not be negative, got %d", path, c.Contexts.Depth)
	}
	if c.Contexts.Depth == 0 {
		c.Contexts.Depth = 2
	}
	if c.Annotations == "" {
		c.Annotations = "annotations"
	}
	if c.Name == "" {
		return nil, fmt.Errorf("workspace: %s: name is required", path)
	}
	seen := map[string]bool{}
	for i, r := range c.Repos {
		if !repoName.MatchString(r.Name) || seen[r.Name] {
			return nil, fmt.Errorf("workspace: repos[%d]: name %q must be unique and match %s", i, r.Name, repoName)
		}
		seen[r.Name] = true
		if r.Path == "" {
			return nil, fmt.Errorf("workspace: repo %s: path is required", r.Name)
		}
		if c.Repos[i].Ref == "" {
			c.Repos[i].Ref = "HEAD"
		}
		for _, p := range r.Plugins {
			if _, ok := c.Plugins[p]; !ok {
				return nil, fmt.Errorf("workspace: repo %s: unknown plugin %q", r.Name, p)
			}
		}
	}
	if c.Schema != nil {
		if len(c.Schema.Command) == 0 {
			return nil, fmt.Errorf("workspace: schema.command is required")
		}
		if c.Schema.PrepareRepo != "" && !seen[c.Schema.PrepareRepo] {
			return nil, fmt.Errorf("workspace: schema.prepare_repo %q is not a repo", c.Schema.PrepareRepo)
		}
		if c.Schema.Prepare != "" && c.Schema.PrepareRepo == "" {
			return nil, fmt.Errorf("workspace: schema.prepare requires schema.prepare_repo (the repo it runs in)")
		}
	}
	return &c, nil
}

// Dir is the directory holding the config file.
func (c *Config) Dir() string { return c.dir }

// RepoPath resolves a repo's checkout path.
func (c *Config) RepoPath(r Repo) string {
	if filepath.IsAbs(r.Path) {
		return r.Path
	}
	return filepath.Join(c.dir, r.Path)
}

// Template is written by `cartographer init`.
const Template = `# cartographer workspace: the repositories one system map covers.
# Plugins run in this directory, so pin their runtimes here (e.g. a mise.toml
# with erlang/elixir/node). {plugins} is the bundled plugin directory.
name = "my-project"
# GitHub repository whose releases hold published snapshots (owner/name).
publish = "my-org/my-project-map"
# Complete snapshot releases publish retains.
keep = 30

[contexts]
# Group modules into contexts by their first N namespace parts.
depth = 2

[[repos]]
name = "my-app"
url = "https://github.com/my-org/my-app"
path = "../my-app"
plugins = ["elixir"]
# Markdown to include (default every tracked *.md). "docs/" includes everything
# under docs/; "docs/*.md" only its top level; ** isn't recursive.
# docs = ["README.md", "docs/"]

[plugins.elixir]
version = "0.1.0"
command = ["elixir", "{plugins}/extract-elixir/extract.exs", "--repo", "{repo}", "--root", "{root}", "--revision", "{revision}", "--source-roots", "lib,test", "--schema-facts", "{schema_facts}"]

# [plugins.javascript]
# version = "0.1.0"
# command = ["node", "{plugins}/extract-javascript/extract.mjs", "--repo", "{repo}", "--root", "{root}", "--revision", "{revision}"]

# Optional: database schema from migrations run against a disposable database.
# [schema]
# prepare_repo = "my-app"
# prepare = "mix ecto.create && mix ecto.migrate"
# env = { MIX_ENV = "test" }
# cache_key = ["priv/repo/migrations"]
# version = "0.1.0"
# command = ["elixir", "{plugins}/extract-postgres-schema/capture.exs", "--database", "cartographer_schema"]
`
