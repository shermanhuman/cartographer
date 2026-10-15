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
