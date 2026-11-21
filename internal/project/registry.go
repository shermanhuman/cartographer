package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/workspace"
)

// StateDir is $CARTOGRAPHER_STATE, else $XDG_STATE_HOME/cartographer or
// ~/.local/state/cartographer: registrations (projects/<name>.json), each
// project's state, lock, log and build scratch.
func StateDir() string {
	if d := os.Getenv("CARTOGRAPHER_STATE"); d != "" {
		return d
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "cartographer")
}

// Registration is a project registered on this machine: its anchor, the
// member list as last read from the anchor, and each member's checkout.
type Registration struct {
	Name   string `json:"name"`
	Anchor string `json:"anchor"` // owner/name
	// Entry is the checkout `project add` was given; members that join
	// later are looked for beside it first.
	Entry   string   `json:"entry"`
	Members []Member `json:"members"`
	// Store is the database file `project add` ran against (absolute): the
	// project's main maps are built into it and no other, and `refresh
	// --head` refuses it.
	Store string `json:"store"`
}

// InStore reports whether db is the store the project builds into.
func (r *Registration) InStore(db string) bool {
	return r.Store != "" && SamePath(r.Store, db)
}

// SamePath reports whether a and b name the same file or directory,
// through symlinks (macOS temp dirs live under /var -> /private/var).
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// Member is one registered member.
type Member struct {
	Repo   string `json:"repo"`             // owner/name
	Branch string `json:"branch,omitempty"` // as the anchor lists it ("" default)
	Path   string `json:"path,omitempty"`   // absolute checkout path ("" none found)
}

// Name is the member's name in the map.
func (m Member) Name() string { return RepoName(m.Repo) }

// URL is where the member is fetched from.
func (m Member) URL() string { return GitHubURL(m.Repo) }

// GitHubURL is https://github.com/<owner>/<name>.
func GitHubURL(slug string) string { return "https://github.com/" + slug }

// Member returns the registered member for slug (case-insensitive).
func (r *Registration) Member(slug string) (Member, bool) {
	for _, m := range r.Members {
		if strings.EqualFold(m.Repo, slug) {
			return m, true
		}
	}
	return Member{}, false
}

func projectsDir(stateDir string) string { return filepath.Join(stateDir, "projects") }

func regPath(stateDir, name string) string {
	return filepath.Join(projectsDir(stateDir), name+".json")
}

// Dir is a project's own state directory (state, lock, log, scratch).
func Dir(stateDir, name string) string {
	return filepath.Join(projectsDir(stateDir), name)
}

// Lock is the project's rebuild lock directory.
func Lock(stateDir, name string) rebuild.Dir {
	return rebuild.Dir{Path: Dir(stateDir, name), Flat: true}
}

// LogPath is where a project's last refresh wrote its output.
func LogPath(stateDir, name string) string { return filepath.Join(Dir(stateDir, name), "log") }

// Load reads a registration (os.ErrNotExist when there is none).
func Load(stateDir, name string) (*Registration, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("project name %q must match %s", name, nameRe)
	}
	b, err := os.ReadFile(regPath(stateDir, name))
	if err != nil {
		return nil, err
	}
	var r Registration
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("project %s: %s: %w", name, regPath(stateDir, name), err)
	}
	if r.Name != name {
		return nil, fmt.Errorf("project %s: %s names project %q", name, regPath(stateDir, name), r.Name)
	}
	return &r, nil
}

// Save writes a registration.
func Save(stateDir string, r *Registration) error {
	if !nameRe.MatchString(r.Name) {
		return fmt.Errorf("project name %q must match %s", r.Name, nameRe)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return workspace.WriteAtomic(regPath(stateDir, r.Name), append(b, '\n'))
}

// Remove deletes a registration and the project's state directory, and
// relabels the project's main maps in s (its store; nil skips this)
// published, so a legacy workspace of the same name is chosen and rebuilt by
// the legacy rules again. Its refs/cartographer/* refs stay in the
// checkouts (harmless; `git update-ref -d` removes them).
func Remove(ctx context.Context, stateDir, name string, s *store.Store) error {
	if _, err := Load(stateDir, name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project %s is not registered", name)
		}
		return err
	}
	if s != nil {
		if _, err := s.Relabel(ctx, name, "main", "published"); err != nil {
			return fmt.Errorf("relabel project %s's maps: %w", name, err)
		}
	}
	if err := os.Remove(regPath(stateDir, name)); err != nil {
		return err
	}
	return os.RemoveAll(Dir(stateDir, name))
}

// List returns every registration, sorted by name. Unreadable ones are
// skipped and reported in the error (the rest still come back).
func List(stateDir string) ([]*Registration, error) {
	if stateDir == "" {
		return nil, nil
	}
	files, _ := filepath.Glob(filepath.Join(projectsDir(stateDir), "*.json"))
	sort.Strings(files)
	var out []*Registration
	var errs []error
	for _, f := range files {
		r, err := Load(stateDir, strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, r)
	}
	return out, errors.Join(errs...)
}
