// Package fresh compares a snapshot's revisions with local checkouts:
// commits behind, and files changed since the snapshot (committed or not).
package fresh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
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
	out, err := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
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

// Checkout is the git repository containing a directory.
type Checkout struct {
	Root   string // top-level directory, "" outside git
	Origin string // origin remote URL, "" if none
}

// Find returns the repository containing dir (two git execs at most).
func Find(ctx context.Context, dir string) Checkout {
	root := Root(ctx, dir)
	if root == "" {
		return Checkout{}
	}
	origin, _ := git(ctx, root, "remote", "get-url", "origin")
	return Checkout{Root: root, Origin: origin}
}

// Match names the repo this checkout holds: by origin URL, else by
// directory name. A name never matches a repo whose URL disagrees with a
// known origin. "" when it holds none of them.
func (c Checkout) Match(repos []Repo) string {
	if name := c.MatchOrigin(repos); name != "" {
		return name
	}
	if c.Root == "" {
		return ""
	}
	want := ""
	if c.Origin != "" {
		want = NormalizeURL(c.Origin)
	}
	base := filepath.Base(c.Root)
	for _, r := range repos {
		if r.Name == base && (want == "" || r.URL == "" || NormalizeURL(r.URL) == want) {
			return r.Name
		}
	}
	return ""
}

// MatchOrigin is Match by origin URL only.
func (c Checkout) MatchOrigin(repos []Repo) string {
	if c.Root == "" || c.Origin == "" {
		return ""
	}
	want := NormalizeURL(c.Origin)
	for _, r := range repos {
		if r.URL != "" && NormalizeURL(r.URL) == want {
			return r.Name
		}
	}
	return ""
}

// Locate finds local checkouts for repos: explicit paths first, then the
// repository containing cwd and its siblings, matched by origin URL or name.
func Locate(ctx context.Context, repos []Repo, cwd string, explicit map[string]string) map[string]string {
	return LocateFrom(ctx, repos, Find(ctx, cwd), explicit)
}

// LocateFrom is Locate starting from an already found checkout. Siblings are
// scanned only while some repo is still unmatched, those named like a repo
// first, so a workspace of sibling clones costs one git exec per repo.
func LocateFrom(ctx context.Context, repos []Repo, cur Checkout, explicit map[string]string) map[string]string {
	// Evidence ranks as in snapshot choice: the current checkout's origin,
	// then [checkouts] paths, then directory names.
	found := map[string]string{}
	if cur.Root != "" {
		if name := cur.MatchOrigin(repos); name != "" {
			found[name] = cur.Root
		}
	}
	for _, r := range repos {
		if p, ok := explicit[r.Name]; ok && found[r.Name] == "" {
			found[r.Name] = ExpandHome(p)
		}
	}
	if cur.Root == "" {
		return found
	}
	claim := func(c Checkout) {
		if name := c.Match(repos); name != "" {
			if _, done := found[name]; !done {
				found[name] = c.Root
			}
		}
	}
	claim(cur)
	if len(found) >= len(repos) {
		return found
	}
	parent := filepath.Dir(cur.Root)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return found
	}
	named := map[string]bool{}
	for _, r := range repos {
		named[r.Name] = true
		if r.URL != "" {
			named[path.Base(NormalizeURL(r.URL))] = true
		}
	}
	var first, rest []string
	for _, e := range entries {
		dir := filepath.Join(parent, e.Name())
		if !e.IsDir() || dir == cur.Root {
			continue
		}
		if named[strings.ToLower(e.Name())] || named[e.Name()] {
			first = append(first, dir)
		} else {
			rest = append(rest, dir)
		}
	}
	for _, dir := range append(first, rest...) {
		if len(found) >= len(repos) {
			break
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		origin, _ := git(ctx, dir, "remote", "get-url", "origin")
		claim(Checkout{Root: dir, Origin: origin})
	}
	return found
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ValidRevision reports whether rev is a full hex object id (SHA-1 or SHA-256).
func ValidRevision(rev string) bool {
	if len(rev) != 40 && len(rev) != 64 {
		return false
	}
	for _, c := range rev {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// Check computes freshness for each repo found locally (three git execs per repo).
func Check(ctx context.Context, repos []Repo, paths map[string]string) *Report {
	rep := &Report{}
	for _, r := range repos {
		st := State{Name: r.Name, Revision: r.Revision, Behind: -1, Changed: map[string]bool{}}
		st.Path = paths[r.Name]
		switch {
		case st.Path == "":
			st.Note = "no local checkout found"
		case !isDir(st.Path):
			st.Note = "configured checkout path not found: " + st.Path
		case r.Revision == "":
			st.Note = "snapshot has no revision"
		case !ValidRevision(r.Revision):
			st.Note = "snapshot revision is not a commit id"
		default:
			// rev-list fails when the revision isn't in the local clone.
			n, err := git(ctx, st.Path, "rev-list", "--count", "--end-of-options", r.Revision+"..HEAD")
			if err != nil {
				st.Note = "snapshot revision not in local clone (git fetch)"
				break
			}
			fmt.Sscanf(n, "%d", &st.Behind)
			// -z: paths verbatim, as facts record them (no C-quoting); --no-renames:
			// a renamed file is changed at its old path too.
			if out, err := gitRaw(ctx, st.Path, "diff", "--name-only", "-z", "--no-renames", "--end-of-options", r.Revision, "--"); err == nil {
				for _, f := range strings.Split(out, "\x00") {
					if f != "" {
						st.Changed[f] = true
					}
				}
			}
			if out, err := gitRaw(ctx, st.Path, "ls-files", "-z", "--others", "--exclude-standard"); err == nil {
				for _, f := range strings.Split(out, "\x00") {
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
