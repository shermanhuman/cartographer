package fresh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, content string) string {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, filepath.Dir(file)), 0o755)
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	sh(t, dir, "git", "add", ".")
	sh(t, dir, "git", "commit", "-qm", file)
	return sh(t, dir, "git", "rev-parse", "HEAD")
}

func TestLocateAndCheck(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	app := filepath.Join(parent, "shop-app")
	worker := filepath.Join(parent, "renamed-worker")
	for _, d := range []string{app, worker} {
		os.MkdirAll(d, 0o755)
		sh(t, d, "git", "init", "-q", "-b", "main")
	}
	sh(t, worker, "git", "remote", "add", "origin", "git@github.com:Example/shop-worker.git")
	snapRev := commit(t, app, "lib/a.ex", "a")
	commit(t, app, "lib/b.ex", "b")
	os.WriteFile(filepath.Join(app, "lib/new.ex"), []byte("x"), 0o644) // untracked
	wRev := commit(t, worker, "lib/w.ex", "w")

	repos := []Repo{
		{Name: "shop-app", URL: "https://github.com/example/shop-app", Revision: snapRev},
		{Name: "shop-worker", URL: "https://github.com/example/shop-worker", Revision: wRev},
		{Name: "missing", Revision: "abc"},
	}
	paths := Locate(ctx, repos, filepath.Join(app, "lib"), nil)
	if paths["shop-app"] == "" || filepath.Base(paths["shop-worker"]) != "renamed-worker" {
		t.Fatalf("locate = %v", paths)
	}
	rep := Check(ctx, repos, paths)
	if rep.Repos[0].Behind != 1 || !rep.Changed("shop-app", "lib/b.ex") || !rep.Changed("shop-app", "lib/new.ex") || rep.Changed("shop-app", "lib/a.ex") {
		t.Errorf("shop-app state = %+v", rep.Repos[0])
	}
	if rep.Repos[1].Behind != 0 || len(rep.Repos[1].Changed) != 0 {
		t.Errorf("worker should be current: %+v", rep.Repos[1])
	}
	if rep.Repos[2].Note != "no local checkout found" {
		t.Errorf("missing: %+v", rep.Repos[2])
	}
	if got := rep.Summary(); got != "shop-app 1 commits behind, 2 files changed" {
		t.Errorf("summary = %q", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, u := range []string{"https://github.com/A/b.git", "git@github.com:a/B.git", "ssh://git@github.com/a/b", "https://github.com/a/b/"} {
		if got := NormalizeURL(u); got != "github.com/a/b" {
			t.Errorf("%s -> %s", u, got)
		}
	}
}

func TestCheckValidatesRevisions(t *testing.T) {
	ctx := context.Background()
	app := t.TempDir()
	sh(t, app, "git", "init", "-q", "-b", "main")
	commit(t, app, "a.txt", "a")
	paths := map[string]string{"x": app}
	for rev, note := range map[string]string{
		"--output=/tmp/pwned":                  "snapshot revision is not a commit id",
		"HEAD":                                 "snapshot revision is not a commit id",
		"abc":                                  "snapshot revision is not a commit id",
		strings.Repeat("c", 40):                "snapshot revision not in local clone (git fetch)",
		strings.Repeat("d", 64):                "snapshot revision not in local clone (git fetch)",
		"g" + strings.Repeat("a", 39):          "snapshot revision is not a commit id",
		sh(t, app, "git", "rev-parse", "HEAD"): "",
	} {
		rep := Check(ctx, []Repo{{Name: "x", Revision: rev}}, paths)
		if got := rep.Repos[0].Note; got != note {
			t.Errorf("%q: note %q, want %q", rev, got, note)
		}
	}
}

func TestLocateScansSiblingsOnlyWhileUnmatched(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	for _, n := range []string{"app", "zz-other"} {
		os.MkdirAll(filepath.Join(parent, n), 0o755)
		sh(t, filepath.Join(parent, n), "git", "init", "-q", "-b", "main")
	}
	sh(t, filepath.Join(parent, "zz-other"), "git", "remote", "add", "origin", "https://github.com/example/app")
	repos := []Repo{{Name: "app", URL: "https://github.com/example/app"}}
	// Every repo matched at cwd: zz-other (same origin) is never looked at.
	if got := Locate(ctx, repos, filepath.Join(parent, "app"), nil); filepath.Base(got["app"]) != "app" {
		t.Errorf("locate = %v", got)
	}
	// Explicit checkouts count as matched.
	got := Locate(ctx, append(repos, Repo{Name: "lib"}), filepath.Join(parent, "app"), map[string]string{"lib": "/somewhere/lib"})
	if got["lib"] != "/somewhere/lib" || filepath.Base(got["app"]) != "app" {
		t.Errorf("explicit = %v", got)
	}
}

func TestConfiguredCheckoutPaths(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	app := filepath.Join(home, "src", "app")
	os.MkdirAll(app, 0o755)
	sh(t, app, "git", "init", "-q", "-b", "main")
	rev := commit(t, app, "a.txt", "a")
	repos := []Repo{{Name: "app", Revision: rev}, {Name: "gone", Revision: rev}}
	found := LocateFrom(ctx, repos, Checkout{}, map[string]string{"app": "~/src/app", "gone": "~/src/gone"})
	if found["app"] != app {
		t.Errorf("~ not expanded: %q", found["app"])
	}
	rep := Check(ctx, repos, found)
	if rep.Repos[0].Note != "" || rep.Repos[0].Behind != 0 {
		t.Errorf("app: %+v", rep.Repos[0])
	}
	if want := "configured checkout path not found: " + filepath.Join(home, "src", "gone"); rep.Repos[1].Note != want {
		t.Errorf("gone: note %q, want %q", rep.Repos[1].Note, want)
	}
}

// A known origin that disagrees with the repo's URL is not that repo, even
// when the directory is named like it; an unknown URL on either side falls
// back to the name.
func TestMatchOriginMismatchIsNotNameMatch(t *testing.T) {
	repos := []Repo{{Name: "app", URL: "https://github.com/other/app"}}
	if got := (Checkout{Root: "/src/app", Origin: "git@github.com:acme/app.git"}).Match(repos); got != "" {
		t.Errorf("mismatched origin matched %q", got)
	}
	if got := (Checkout{Root: "/src/app"}).Match(repos); got != "app" {
		t.Errorf("no origin: %q", got)
	}
	if got := (Checkout{Root: "/src/app", Origin: "https://github.com/acme/app"}).Match([]Repo{{Name: "app"}}); got != "app" {
		t.Errorf("repo without URL: %q", got)
	}

	// Sibling discovery uses the same rule.
	ctx := context.Background()
	parent := t.TempDir()
	for _, n := range []string{"cur", "app"} {
		os.MkdirAll(filepath.Join(parent, n), 0o755)
		sh(t, filepath.Join(parent, n), "git", "init", "-q", "-b", "main")
	}
	sh(t, filepath.Join(parent, "app"), "git", "remote", "add", "origin", "https://github.com/acme/app")
	if got := Locate(ctx, repos, filepath.Join(parent, "cur"), nil); got["app"] != "" {
		t.Errorf("sibling with another origin claimed: %v", got)
	}
}

// Git quotes non-ASCII paths unless -z; facts carry them raw.
func TestCheckKeepsUnicodePathsRaw(t *testing.T) {
	ctx := context.Background()
	app := t.TempDir()
	sh(t, app, "git", "init", "-q", "-b", "main")
	rev := commit(t, app, "lib/café.ex", "a")
	os.WriteFile(filepath.Join(app, "lib/café.ex"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(app, "lib/naïve thing.ex"), []byte("x"), 0o644) // untracked
	rep := Check(ctx, []Repo{{Name: "app", Revision: rev}}, map[string]string{"app": app})
	if !rep.Changed("app", "lib/café.ex") || !rep.Changed("app", "lib/naïve thing.ex") {
		t.Errorf("changed = %v", rep.Repos[0].Changed)
	}
}

// A rename changes the file facts cite at the old path; git's default rename
// detection would list only the new one.
func TestCheckReportsRenamedFromPath(t *testing.T) {
	ctx := context.Background()
	app := t.TempDir()
	sh(t, app, "git", "init", "-q", "-b", "main")
	rev := commit(t, app, "lib/a.ex", "defmodule A do\nend\n")
	sh(t, app, "git", "mv", "lib/a.ex", "lib/b.ex")
	sh(t, app, "git", "commit", "-qm", "rename")
	rep := Check(ctx, []Repo{{Name: "app", Revision: rev}}, map[string]string{"app": app})
	if !rep.Changed("app", "lib/a.ex") || !rep.Changed("app", "lib/b.ex") {
		t.Errorf("changed = %v", rep.Repos[0].Changed)
	}
}
