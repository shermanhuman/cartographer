// Package project maps registered projects: repositories whose fetched
// default branches cartographer builds into one map, each carrying its
// hand-written settings and annotations in a .cartographer/ folder, with one
// anchor repository holding the member list.
//
// A project is registered per machine (project add); every build fetches
// each member into cartographer's own refs (refs/cartographer/<branch>),
// freezes those commits, reads the settings from them and builds in the
// state directory. Nothing is ever written into a checkout's files, branches
// or remote-tracking refs, and nothing is cloned.
package project

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// SettingsDir is the folder of hand-written settings in each mapped repo.
const SettingsDir = ".cartographer"

// SettingsFile is the repo's settings, relative to its root.
const SettingsFile = SettingsDir + "/repo.toml"

// AnnotationsDir holds the repo's annotation files (*.json).
const AnnotationsDir = SettingsDir + "/annotations"

// Extractors a repo may list in `extract`. The config-URL extractor runs for
// every repo and isn't listed.
var Extractors = []string{"go", "rust", "elixir", "javascript", "kubernetes"}

// Languages whose version a repo's pin files give its extractors.
var Languages = []string{"go", "rust", "elixir", "erlang", "node"}

// Settings is a repo's .cartographer/repo.toml.
type Settings struct {
	// Project names the project the repo belongs to.
	Project string `toml:"project"`
	// Anchor is the repo (owner/name) holding the member list; empty means
	// this repo is the anchor.
	Anchor string `toml:"anchor"`
	// Members is the member list (the anchor only).
	Members []MemberSpec `toml:"members"`
	// Extract lists the extractors to run (see Extractors).
	Extract []string `toml:"extract"`
	// Docs are the Markdown patterns to include (as workspace.Repo.Docs):
	// omitted or [] means every tracked *.md, false means none.
	Docs Docs `toml:"docs"`
	// SourceRoots are passed to the Elixir and JavaScript extractors.
	SourceRoots []string `toml:"source_roots"`
	// Images are the container image names (last path segment, no tag) this
	// repo builds, for linking deployments to it.
	Images []string `toml:"images"`
	// Schema declares the schema step, run in this repo.
	Schema *SchemaSettings `toml:"schema"`
	Rust   RustSettings    `toml:"rust"`
	// ContextsDepth groups modules into contexts (the anchor's applies;
	// default 2).
	ContextsDepth int `toml:"contexts_depth"`
}

// MemberSpec is one entry of the anchor's member list.
type MemberSpec struct {
	Repo   string `toml:"repo"`   // owner/name
	Branch string `toml:"branch"` // default: the remote's default branch
}

// SchemaSettings is the schema step: Prepare runs in this repo's archive
// (e.g. migrations against a disposable database), then Capture prints the
// schema facts document. Both are skipped while the files under CacheKey
// (or, with none, the commit) and the commands are unchanged. Postgres =
// "managed" runs both against a throwaway PostgreSQL cluster (the release's
// Postgres pin, from mise) whose {pg_host} and {pg_port} expand in Env and
// Capture, with its programs (psql) on their PATH.
type SchemaSettings struct {
	Prepare  string            `toml:"prepare"`
	Env      map[string]string `toml:"env"`
	CacheKey []string          `toml:"cache_key"`
	Capture  []string          `toml:"capture"`
	Postgres string            `toml:"postgres"`
}

// Managed reports postgres = "managed".
func (s *SchemaSettings) Managed() bool { return s != nil && s.Postgres == "managed" }

// RustSettings configure the Rust extractor.
type RustSettings struct {
	// BuildScripts runs build scripts and proc macros (default true).
	BuildScripts *bool `toml:"build_scripts"`
}

// BuildScriptsOn reports the build_scripts setting (default on).
func (r RustSettings) BuildScriptsOn() bool { return r.BuildScripts == nil || *r.BuildScripts }

// Docs is the `docs` setting: a pattern list, or false for none.
type Docs struct {
	Patterns []string
	None     bool
}

// UnmarshalTOML accepts a list of strings or false.
func (d *Docs) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case bool:
		if v {
			return errors.New("docs: want a list of patterns, or false for none")
		}
		d.None = true
		return nil
	case []any:
		for _, p := range v {
			s, ok := p.(string)
			if !ok {
				return fmt.Errorf("docs: %v is not a string", p)
			}
			d.Patterns = append(d.Patterns, s)
		}
		return nil
	}
	return fmt.Errorf("docs: want a list of patterns, or false for none, got %T", v)
}

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	slugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// ValidSlug reports whether s is owner/name.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// RepoName is the name part of owner/name, the repo's name in the map.
func RepoName(slug string) string {
	_, name, _ := strings.Cut(slug, "/")
	return name
}

// ParseSettings decodes a repo.toml strictly (unknown keys fail) and
// validates it for the repo self (owner/name).
func ParseSettings(b []byte, self string) (*Settings, error) {
	var s Settings
	md, err := toml.Decode(string(b), &s)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("unknown keys %v", und)
	}
	if s.Project == "" {
		return nil, errors.New("project is required")
	}
	if !nameRe.MatchString(s.Project) {
		return nil, fmt.Errorf("project %q must match %s", s.Project, nameRe)
	}
	if strings.EqualFold(s.Anchor, self) {
		s.Anchor = ""
	}
	if s.Anchor != "" && !ValidSlug(s.Anchor) {
		return nil, fmt.Errorf("anchor %q must be owner/name", s.Anchor)
	}
	if s.Anchor != "" && len(s.Members) > 0 {
		return nil, fmt.Errorf("members belong in the anchor's repo.toml (%s), not here", s.Anchor)
	}
	seen := map[string]bool{}
	for i, m := range s.Members {
		if !ValidSlug(m.Repo) {
			return nil, fmt.Errorf("members[%d]: repo %q must be owner/name", i, m.Repo)
		}
		if m.Branch != "" && (strings.HasPrefix(m.Branch, "-") || strings.ContainsAny(m.Branch, " :^~?*[\\")) {
			return nil, fmt.Errorf("members[%d]: branch %q is not a branch name", i, m.Branch)
		}
		name := strings.ToLower(RepoName(m.Repo))
		if seen[name] {
			return nil, fmt.Errorf("members[%d]: a repo named %s is listed twice (names are unique within a project)", i, RepoName(m.Repo))
		}
		seen[name] = true
	}
	for _, e := range s.Extract {
		if !slices.Contains(Extractors, e) {
			return nil, fmt.Errorf("extract: unknown extractor %q (want %s)", e, strings.Join(Extractors, ", "))
		}
	}
	if s.ContextsDepth < 0 {
		return nil, fmt.Errorf("contexts_depth must not be negative, got %d", s.ContextsDepth)
	}
	if s.Schema != nil && len(s.Schema.Capture) == 0 {
		return nil, errors.New("schema.capture is required")
	}
	if s.Schema != nil && s.Schema.Postgres != "" && !s.Schema.Managed() {
		return nil, fmt.Errorf(`schema.postgres %q: the only value is "managed"`, s.Schema.Postgres)
	}
	for _, r := range s.SourceRoots {
		if r == "" || strings.HasPrefix(r, "/") || slices.Contains(strings.Split(r, "/"), "..") || strings.Contains(r, ",") {
			return nil, fmt.Errorf("source_roots: %q must be a relative path inside the repo", r)
		}
	}
	return &s, nil
}

// MemberList is the anchor's member list with the anchor itself included
// (first when it isn't listed).
func (s *Settings) MemberList(anchor string) []MemberSpec {
	list := slices.Clone(s.Members)
	if !slices.ContainsFunc(list, func(m MemberSpec) bool { return strings.EqualFold(m.Repo, anchor) }) {
		list = append([]MemberSpec{{Repo: anchor}}, list...)
	}
	return list
}
