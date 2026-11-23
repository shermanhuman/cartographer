package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/fresh"
)

// Locator finds members' checkouts (never clones): a [checkouts] entry of
// the user config (by owner/name, then by name), then a sibling of Near
// (the checkout `project add` was given), then the Roots searched two
// levels deep, matching any remote whose URL normalizes to the member's
// GitHub URL. A main working tree beats a linked worktree wherever it is
// found; among equals a sibling comes first, then the shortest path.
type Locator struct {
	Checkouts map[string]string
	Near      string
	Roots     []string
	// PreferNear makes any sibling of Near win, a linked worktree too, over
	// the roots: `refresh --head` from a PR worktree reads the PR
	// worktrees beside it, not the main checkouts.
	PreferNear bool

	scanned map[string][]string // normalized URL -> candidate checkouts under Roots
}

// Find returns slug's checkout, or "".
func (l *Locator) Find(ctx context.Context, slug string) string {
	for _, k := range []string{slug, RepoName(slug)} {
		for key, p := range l.Checkouts {
			if strings.EqualFold(key, k) {
				if p = filepath.Clean(fresh.ExpandHome(p)); isCheckout(p) && filepath.IsAbs(p) {
					return p
				}
			}
		}
	}
	want := fresh.NormalizeURL(GitHubURL(slug))
	var siblings []string
	if l.Near != "" {
		if entries, err := os.ReadDir(filepath.Dir(l.Near)); err == nil {
			for _, e := range entries {
				dir := filepath.Join(filepath.Dir(l.Near), e.Name())
				if e.IsDir() && isCheckout(dir) && hasRemote(ctx, dir, want) {
					siblings = append(siblings, dir)
				}
			}
		}
	}
	// A sibling main working tree needs no scan of the roots.
	if p := best(siblings); p != "" && (!isWorktree(p) || l.PreferNear) {
		return p
	}
	if l.scanned == nil {
		l.scanned = map[string][]string{}
		for _, root := range l.Roots {
			for _, dir := range candidates(root) {
				for _, u := range remotes(ctx, dir) {
					l.scanned[u] = append(l.scanned[u], dir)
				}
			}
		}
	}
	if p := best(l.scanned[want]); p != "" && !isWorktree(p) {
		return p
	}
	// Only linked worktrees: a sibling first.
	if p := best(siblings); p != "" {
		return p
	}
	return best(l.scanned[want])
}

// CloneCommand is the clone a member without a checkout needs.
func CloneCommand(slug string, roots []string) string {
	root := "~/github"
	if len(roots) > 0 {
		root = roots[0]
	}
	return "git clone " + GitHubURL(slug) + " " + filepath.Join(root, RepoName(slug))
}

// best picks among matching checkouts: main working trees over linked
// worktrees, then the shortest path, then the first by name.
func best(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		wi, wj := isWorktree(dirs[i]), isWorktree(dirs[j])
		if wi != wj {
			return !wi
		}
		if len(dirs[i]) != len(dirs[j]) {
			return len(dirs[i]) < len(dirs[j])
		}
		return dirs[i] < dirs[j]
	})
	return dirs[0]
}

// candidates lists git checkouts one and two levels below root.
func candidates(root string) []string {
	var out []string
	l1, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range l1 {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d1 := filepath.Join(root, e.Name())
		if isCheckout(d1) {
			out = append(out, d1)
			continue
		}
		l2, err := os.ReadDir(d1)
		if err != nil {
			continue
		}
		for _, e2 := range l2 {
			if d2 := filepath.Join(d1, e2.Name()); e2.IsDir() && !strings.HasPrefix(e2.Name(), ".") && isCheckout(d2) {
				out = append(out, d2)
			}
		}
	}
	return out
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// isWorktree reports a linked worktree (.git is a file).
func isWorktree(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && !fi.IsDir()
}

// remotes returns the normalized URLs of every remote of the checkout.
func remotes(ctx context.Context, dir string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get-regexp", `^remote\..*\.url$`).Output()
	if err != nil {
		return nil
	}
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, u, ok := strings.Cut(line, " "); ok {
			urls = append(urls, fresh.NormalizeURL(u))
		}
	}
	return urls
}

func hasRemote(ctx context.Context, dir, want string) bool {
	for _, u := range remotes(ctx, dir) {
		if u == want {
			return true
		}
	}
	return false
}

// Identify returns the GitHub owner/name a checkout's remotes name (origin
// first), or "".
func Identify(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get-regexp", `^remote\..*\.url$`).Output()
	if err != nil {
		return ""
	}
	var first string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, u, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		slug := githubSlug(u)
		if slug == "" {
			continue
		}
		if key == "remote.origin.url" {
			return slug
		}
		if first == "" {
			first = slug
		}
	}
	return first
}

// githubSlug is the owner/name of a github.com remote URL, in the URL's
// own case (fetch URLs and url.insteadOf rules are case-sensitive), or "".
func githubSlug(u string) string {
	n := fresh.NormalizeURL(u)
	if !strings.HasPrefix(n, "github.com/") {
		return ""
	}
	// NormalizeURL lowercases; cut the same suffix of the original.
	raw := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), ".git")
	slug := n[len("github.com/"):]
	if len(raw) < len(slug) || !strings.EqualFold(raw[len(raw)-len(slug):], slug) {
		return ""
	}
	slug = raw[len(raw)-len(slug):]
	if !ValidSlug(slug) {
		return ""
	}
	return slug
}
