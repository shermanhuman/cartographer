package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	// r.Store is resolved by Load; db is the caller's explicit choice.
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
	// A default store recorded under an earlier schema means this schema's
	// file now; resolving here gives every reader the same answer.
	r.Store = store.Resolve(r.Store)
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

// Register saves reg, replacing any registration of its name, under the
// project's lock: a running cycle saves the registration it loaded (with
// the member list it read), which would undo the replacement.
func Register(stateDir string, reg *Registration) error {
	if !nameRe.MatchString(reg.Name) {
		return fmt.Errorf("project name %q must match %s", reg.Name, nameRe)
	}
	unlock, err := Lock(stateDir, reg.Name).Lock(reg.Name)
	if errors.Is(err, rebuild.ErrRunning) {
		return fmt.Errorf("a refresh of project %s is running (log: %s); register it again when that is done", reg.Name, LogPath(stateDir, reg.Name))
	} else if err != nil {
		return err
	}
	defer unlock()
	return Save(stateDir, reg)
}

// Remove deletes a registration, the project's state directory and its
// main maps in s (its store; nil skips this), so a legacy workspace of the
// same name answers from its own map at once and is rebuilt by the legacy
// rules again (rollback restores carto-0.1.db; it doesn't need these
// maps). Its refs/cartographer/* refs stay in the checkouts (harmless;
// `git update-ref -d` removes them).
func Remove(ctx context.Context, stateDir, name string, s *store.Store) error {
	if _, err := Load(stateDir, name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project %s is not registered", name)
		}
		return err
	}
	// Under the project's lock: a running cycle would recreate the state.
	unlock, err := Lock(stateDir, name).Lock(name)
	if errors.Is(err, rebuild.ErrRunning) {
		return fmt.Errorf("a refresh of project %s is running (log: %s); remove it when that is done", name, LogPath(stateDir, name))
	} else if err != nil {
		return err
	}
	defer unlock()
	if s != nil {
		if _, err := s.DeleteSource(ctx, name, "main"); err != nil {
			return fmt.Errorf("delete project %s's maps: %w", name, err)
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
	out, problems, err := ListDetailed(stateDir)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, p := range problems {
		errs = append(errs, errors.New(p.Error))
	}
	return out, errors.Join(errs...)
}

// ListProblem is one registration ListDetailed couldn't read.
type ListProblem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// ListDetailed is List for callers that must tell "no projects" from "can't
// tell": a missing projects directory means none, any other directory read
// error is returned, and each unreadable registration is its own problem
// (the readable ones still come back, sorted by name).
func ListDetailed(stateDir string) ([]*Registration, []ListProblem, error) {
	if stateDir == "" {
		return nil, nil, nil
	}
	dir := projectsDir(stateDir)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("listing projects: %w", err)
	}
	var out []*Registration
	var problems []ListProblem
	for _, e := range ents { // ReadDir sorts by file name
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		r, err := Load(stateDir, name)
		if err != nil {
			problems = append(problems, ListProblem{File: filepath.Join(dir, e.Name()), Error: err.Error()})
			continue
		}
		out = append(out, r)
	}
	return out, problems, nil
}
