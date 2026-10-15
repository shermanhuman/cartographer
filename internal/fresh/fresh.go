// Package fresh compares a snapshot's revisions with local checkouts:
// commits behind, and files changed since the snapshot (committed or not).
package fresh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is a repository recorded in a snapshot.
type Repo struct {
	Name, URL, Revision string
}

// State is one repository's freshness.
type State struct {
	Name     string
	Path     string // local checkout, "" if not found
	Revision string // snapshot revision
	Behind   int    // commits from Revision to local HEAD (-1 unknown)
	Changed  map[string]bool
	Note     string // why the state is unknown
}

// Report covers every repository in a snapshot.
type Report struct{ Repos []State }

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// Root returns the git top-level directory containing dir, or "".
func Root(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

// NormalizeURL reduces git remote URLs to host/owner/name.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, p)
	}
	u = strings.TrimPrefix(u, "git@")
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	return strings.Replace(u, ":", "/", 1)
}

// Locate finds local checkouts for repos: explicit paths first, then the
// repository containing cwd and its siblings, matched by origin URL or name.
func Locate(ctx context.Context, repos []Repo, cwd string, explicit map[string]string) map[string]string {
	found := map[string]string{}
	for _, r := range repos {
		if p, ok := explicit[r.Name]; ok {
			found[r.Name] = p
		}
	}
	root := Root(ctx, cwd)
	if root == "" {
		return found
	}
	candidates := []string{root}
	if entries, err := os.ReadDir(filepath.Dir(root)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates, filepath.Join(filepath.Dir(root), e.Name()))
			}
		}
	}
	byURL := map[string]string{}
	for _, r := range repos {
		if r.URL != "" {
			byURL[NormalizeURL(r.URL)] = r.Name
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, ".git")); err != nil {
			continue
		}
		origin, _ := git(ctx, c, "remote", "get-url", "origin")
		name := byURL[NormalizeURL(origin)]
		if name == "" {
			for _, r := range repos {
				if r.Name == filepath.Base(c) {
					name = r.Name
				}
			}
		}
		if name != "" {
			if _, done := found[name]; !done {
				found[name] = c
			}
		}
	}
	return found
}

// Check computes freshness for each repo found locally.
func Check(ctx context.Context, repos []Repo, paths map[string]string) *Report {
	rep := &Report{}
	for _, r := range repos {
		st := State{Name: r.Name, Revision: r.Revision, Behind: -1, Changed: map[string]bool{}}
		st.Path = paths[r.Name]
		switch {
		case st.Path == "":
			st.Note = "no local checkout found"
		case r.Revision == "":
			st.Note = "snapshot has no revision"
		default:
			if _, err := git(ctx, st.Path, "cat-file", "-e", r.Revision+"^{commit}"); err != nil {
				st.Note = "snapshot revision not in local clone (git fetch)"
				break
			}
			if n, err := git(ctx, st.Path, "rev-list", "--count", r.Revision+"..HEAD"); err == nil {
				fmt.Sscanf(n, "%d", &st.Behind)
			}
			if out, err := git(ctx, st.Path, "diff", "--name-only", r.Revision); err == nil {
				for _, f := range strings.Split(out, "\n") {
					if f != "" {
						st.Changed[f] = true
					}
				}
			}
			if out, err := git(ctx, st.Path, "ls-files", "--others", "--exclude-standard"); err == nil {
				for _, f := range strings.Split(out, "\n") {
					if f != "" {
						st.Changed[f] = true
					}
				}
			}
		}
		rep.Repos = append(rep.Repos, st)
	}
	return rep
}

// Changed reports whether repo/file changed since the snapshot.
func (r *Report) Changed(repo, file string) bool {
	for _, s := range r.Repos {
		if s.Name == repo {
			return s.Changed[file]
		}
	}
	return false
}

// Summary is a one-line staleness note ("" when everything is current).
func (r *Report) Summary() string {
	var parts []string
	for _, s := range r.Repos {
		switch {
		case s.Behind > 0 && len(s.Changed) > 0:
			parts = append(parts, fmt.Sprintf("%s %d commits behind, %d files changed", s.Name, s.Behind, len(s.Changed)))
		case s.Behind > 0:
			parts = append(parts, fmt.Sprintf("%s %d commits behind", s.Name, s.Behind))
		case len(s.Changed) > 0:
			parts = append(parts, fmt.Sprintf("%s %d files changed locally", s.Name, len(s.Changed)))
		}
	}
	return strings.Join(parts, "; ")
}

// Lines describe every repository, for `status`.
func (r *Report) Lines() []string {
	var out []string
	for _, s := range r.Repos {
		rev := s.Revision
		if len(rev) > 7 {
			rev = rev[:7]
		}
		switch {
		case s.Note != "":
			out = append(out, fmt.Sprintf("%s@%s: %s", s.Name, rev, s.Note))
		case s.Behind == 0 && len(s.Changed) == 0:
			out = append(out, fmt.Sprintf("%s@%s: current (%s)", s.Name, rev, s.Path))
		default:
			out = append(out, fmt.Sprintf("%s@%s: %d commits behind, %d files changed ⚠ (%s)", s.Name, rev, s.Behind, len(s.Changed), s.Path))
		}
	}
	return out
}
