// Package bundle reads and writes the on-disk layout of a snapshot:
//
//	manifest.json
//	facts/*.json              facts documents listed in the manifest
//	docs/<repo>/<path>.md     tracked Markdown, copied at build time
//	annotations/*.json        human-reviewed journeys and notes
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// Format is the snapshot format this package writes; MinFormat is the oldest
// it reads. Format 2 adds the manifest's source, inventory, tools and
// inputs digest, and allows facts version 2.
const (
	Format    = 2
	MinFormat = 1
)

// SnapshotID names a snapshot of workspace built at t:
// snapshot-<UTC second>-<WorkspaceTag>. Lexical order is chronological
// within a workspace, and workspaces built in the same second differ.
func SnapshotID(workspace string, t time.Time) string {
	return "snapshot-" + t.UTC().Format("20060102T150405Z") + "-" + WorkspaceTag(workspace)
}

// WorkspaceTag is the 8 hex digits of sha256(workspace) that SnapshotID ends with.
func WorkspaceTag(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(sum[:4])
}

var idPattern = regexp.MustCompile(`^snapshot-\d{8}T\d{6}Z-([0-9a-f]{8})$`)

// IDWorkspaceTag returns the workspace tag an id made by SnapshotID carries,
// or "" for other ids (older timestamp-only ones included).
func IDWorkspaceTag(id string) string {
	if m := idPattern.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

type Manifest struct {
	SnapshotFormat int        `json:"snapshot_format"`
	ID             string     `json:"id"`
	CreatedAt      string     `json:"created_at"`
	Workspace      string     `json:"workspace"`
	Repos          []Repo     `json:"repos"`
	Facts          []string   `json:"facts"`
	Contexts       ContextCfg `json:"contexts"`

	// Format 2.
	// Source says what the build mapped: "main" (each repo's fetched default
	// branch), "head" (the checkouts as they are), or "" (a published or
	// explicit build).
	Source       string            `json:"source,omitempty"`
	Inventory    []Inventory       `json:"inventory,omitempty"`
	Tools        map[string]string `json:"tools,omitempty"` // tool -> resolved version
	InputsDigest string            `json:"inputs_digest,omitempty"`
}

// Inventory counts a repository's tracked files by extension at the mapped
// revision, so coverage can be computed without the checkout.
type Inventory struct {
	Repo  string `json:"repo"`
	Ext   string `json:"ext"` // ".go", ".rs"; "" for files without one
	Files int    `json:"files"`
}

type Repo struct {
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// ContextCfg groups modules into contexts by their first Depth namespace parts.
type ContextCfg struct {
	Depth int `json:"depth,omitempty"`
}

type Doc struct {
	Repo    string
	Path    string
	Content string
}

// Annotation is a human-written explanation that cites map entities. Only
// the fields below are interpreted; Raw keeps the authored JSON verbatim, so
// richer presentation fields (groups, owners, outcomes, ...) reach the UI.
type Annotation struct {
	Raw json.RawMessage `json:"-"`

	Kind     string            `json:"kind"`
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Summary  string            `json:"summary,omitempty"`
	Body     string            `json:"body,omitempty"`
	Cites    []string          `json:"cites,omitempty"`
	Reviewed map[string]string `json:"reviewed,omitempty"`
	Steps    []Step            `json:"steps,omitempty"`
}

type Step struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Body     string            `json:"body,omitempty"`
	Cites    []string          `json:"cites,omitempty"`
	Reviewed map[string]string `json:"reviewed,omitempty"`
}

type Bundle struct {
	Manifest    Manifest
	Facts       []*facts.Doc
	Docs        []Doc
	Annotations []Annotation
}

// Read loads and validates a snapshot directory.
func Read(dir string) (*Bundle, error) {
	b := &Bundle{}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &b.Manifest); err != nil {
		return nil, fmt.Errorf("bundle: manifest: %w", err)
	}
	m := &b.Manifest
	if m.SnapshotFormat < MinFormat || m.SnapshotFormat > Format {
		return nil, fmt.Errorf("bundle: snapshot_format %d not supported (want %d to %d; a newer snapshot needs a newer cartographer)", m.SnapshotFormat, MinFormat, Format)
	}
	if m.SnapshotFormat == 1 && (m.Source != "" || len(m.Inventory) > 0 || len(m.Tools) > 0 || m.InputsDigest != "") {
		return nil, fmt.Errorf("bundle: snapshot_format 1 manifest carries format 2 fields")
	}
	if m.ID == "" || m.CreatedAt == "" {
		return nil, fmt.Errorf("bundle: manifest needs id and created_at")
	}
	if tag := IDWorkspaceTag(m.ID); tag != "" && tag != WorkspaceTag(m.Workspace) {
		return nil, fmt.Errorf("bundle: id %s names another workspace, not workspace %q", m.ID, m.Workspace)
	}
	if m.Contexts.Depth < 0 {
		return nil, fmt.Errorf("bundle: manifest contexts.depth must not be negative, got %d", m.Contexts.Depth)
	}
	if m.Contexts.Depth == 0 {
		m.Contexts.Depth = 2
	}

	repos := map[string]bool{}
	for _, r := range m.Repos {
		repos[r.Name] = true
	}
	// Ids are unique across the workspace: kind -> id -> facts file.
	seen := map[string]map[string]string{"module": {}, "function": {}, "table": {}}
	// Routes and functions are both edge endpoints, so they share one namespace.
	seen["route"] = seen["function"]
	claim := func(kind, id, rel string) error {
		if prev, ok := seen[kind][id]; ok {
			return fmt.Errorf("bundle: duplicate %s id %q in %s and %s", kind, id, prev, rel)
		}
		seen[kind][id] = rel
		return nil
	}
	// Facts paths come from the manifest: they must stay inside dir, through
	// symlinks too (os.Root).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, rel := range m.Facts {
		name, ok := cleanRel(rel)
		if !ok {
			return nil, fmt.Errorf("bundle: unsafe facts path %q", rel)
		}
		f, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		d, err := facts.Decode(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("bundle: %s: %w", rel, err)
		}
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("bundle: %s: %w", rel, err)
		}
		if d.FactsVersion > 1 && m.SnapshotFormat == 1 {
			return nil, fmt.Errorf("bundle: %s: facts_version %d in a snapshot_format 1 snapshot", rel, d.FactsVersion)
		}
		if d.Repo != "" && !repos[d.Repo] {
			return nil, fmt.Errorf("bundle: %s: repo %q is not in the manifest", rel, d.Repo)
		}
		for _, mo := range d.Modules {
			if err := claim("module", mo.ID, rel); err != nil {
				return nil, err
			}
		}
		for _, fn := range d.Functions {
			if err := claim("function", fn.ID, rel); err != nil {
				return nil, err
			}
		}
		for _, t := range d.Tables {
			if err := claim("table", t.ID, rel); err != nil {
				return nil, err
			}
		}
		for _, r := range d.Routes {
			if err := claim("route", r.ID, rel); err != nil {
				return nil, err
			}
		}
		b.Facts = append(b.Facts, d)
	}

	docsDir := filepath.Join(dir, "docs")
	err = filepath.WalkDir(docsDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == docsDir {
				return filepath.SkipDir
			}
			return err
		}
		if e.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(docsDir, p)
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) != 2 {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.Docs = append(b.Docs, Doc{Repo: parts[0], Path: parts[1], Content: string(content)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(b.Docs, func(i, j int) bool {
		if b.Docs[i].Repo != b.Docs[j].Repo {
			return b.Docs[i].Repo < b.Docs[j].Repo
		}
		return b.Docs[i].Path < b.Docs[j].Path
	})

	files, _ := filepath.Glob(filepath.Join(dir, "annotations", "*.json"))
	sort.Strings(files)
	annFile := map[string]string{}
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Annotations []json.RawMessage `json:"annotations"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("bundle: %s: %w", filepath.Base(p), err)
		}
		for i, r := range doc.Annotations {
			var a Annotation
			if err := json.Unmarshal(r, &a); err != nil {
				return nil, fmt.Errorf("bundle: %s: annotations[%d]: %w", filepath.Base(p), i, err)
			}
			if a.ID == "" || a.Kind == "" {
				return nil, fmt.Errorf("bundle: %s: annotations[%d] needs id and kind", filepath.Base(p), i)
			}
			if prev, ok := annFile[a.ID]; ok {
				return nil, fmt.Errorf("bundle: duplicate annotation id %q in %s and %s", a.ID, prev, filepath.Base(p))
			}
			annFile[a.ID] = filepath.Base(p)
			a.Raw = r
			b.Annotations = append(b.Annotations, a)
		}
	}
	return b, nil
}

// MapID is the identity of a built snapshot's content: the sha256 of every
// file under dir in sorted relative-path order, each framed as path length,
// path, content length and content, with the manifest's id and created_at
// left out, plus the version of the binary that built it. Two snapshots with
// the same MapID answer every query the same way.
func MapID(dir, version string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		} else if !e.IsDir() {
			return fmt.Errorf("bundle: %s is not a regular file", p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	frame := func(b []byte) {
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	frame([]byte("cartographer " + version))
	for _, rel := range paths {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		if rel == "manifest.json" {
			var m map[string]any
			if err := json.Unmarshal(content, &m); err != nil {
				return "", fmt.Errorf("bundle: manifest: %w", err)
			}
			delete(m, "id")
			delete(m, "created_at")
			if content, err = json.Marshal(m); err != nil { // map keys sort
				return "", err
			}
		}
		frame([]byte(rel))
		frame(content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
