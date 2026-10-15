package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
)

func run(t *testing.T, dir string, args ...string) string {
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

func write(t *testing.T, p, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setup makes a git repo "shop-app" and a workspace whose plugins cat the
// mini fixture's facts, and whose schema prepare appends to a log.
func setup(t *testing.T) (ws, repo, log string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "shop-app")
	write(t, filepath.Join(repo, "lib/shop/orders.ex"), "defmodule Shop.Orders do end\n")
	write(t, filepath.Join(repo, "docs/orders.md"), "# Orders\nSee Shop.Orders.\n")
	write(t, filepath.Join(repo, "priv/repo/migrations/001.exs"), "create table(:orders)\n")
	write(t, filepath.Join(repo, "README.md"), "# Shop\n")
	run(t, repo, "git", "init", "-q", "-b", "main")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "init")
	write(t, filepath.Join(repo, "untracked.md"), "# not tracked\n")

	ws = filepath.Join(root, "ws")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	log = filepath.Join(ws, "prepare.log")
	write(t, filepath.Join(ws, "annotations/journeys.json"), mustRead(t, filepath.Join(fixture, "annotations/journeys.json")))
	write(t, filepath.Join(ws, FileName), `
name = "mini"
publish = "example/mini-map"
[contexts]
depth = 2
[[repos]]
name = "shop-app"
url = "https://github.com/example/shop-app"
path = "../shop-app"
plugins = ["fake"]
[plugins.fake]
version = "1"
command = ["sh", "-c", "test -f \"$1/lib/shop/orders.ex\" && test -n \"$SF\" && test -f ./cartographer.toml && cat `+fixture+`/facts/shop-app.json", "plugin", "{root}"]
env = { SF = "set" }
[schema]
prepare_repo = "shop-app"
prepare = "echo prepared >> `+log+`"
cache_key = ["priv/repo/migrations"]
version = "1"
command = ["cat", "`+fixture+`/facts/schema.json"]
`)
	return ws, repo, log
}

func mustRead(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildProducesReadableSnapshot(t *testing.T) {
	ws, repo, log := setup(t)
	c, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	out := filepath.Join(t.TempDir(), "snap")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	m, err := Build(context.Background(), c, BuildOptions{Out: out, CacheDir: cache, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "snapshot-20261015T120000Z" || m.Repos[0].Revision != run(t, repo, "git", "rev-parse", "HEAD") {
		t.Errorf("manifest = %+v", m)
	}
	b, err := bundle.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Facts) != 2 || len(b.Annotations) != 1 {
		t.Errorf("facts=%d annotations=%d", len(b.Facts), len(b.Annotations))
	}
	var docs []string
	for _, d := range b.Docs {
		docs = append(docs, d.Path)
	}
	if strings.Join(docs, ",") != "README.md,docs/orders.md" {
		t.Errorf("docs = %v (untracked files must be excluded)", docs)
	}

	// Second build reuses the cached schema; prepare ran once.
	if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s2"), CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(mustRead(t, log), "prepared"); n != 1 {
		t.Errorf("prepare ran %d times, want 1 (cache)", n)
	}
	// A migration change invalidates the cache.
	write(t, filepath.Join(repo, "priv/repo/migrations/002.exs"), "alter table(:orders)\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "m2")
	if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s3"), CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(mustRead(t, log), "prepared"); n != 2 {
		t.Errorf("prepare ran %d times, want 2", n)
	}
}

func TestBuildRefOverride(t *testing.T) {
	ws, repo, _ := setup(t)
	first := run(t, repo, "git", "rev-parse", "HEAD")
	write(t, filepath.Join(repo, "lib/shop/billing.ex"), "defmodule Shop.Billing do end\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "two")
	c, _ := Load(ws)
	m, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: t.TempDir(), Refs: map[string]string{"shop-app": first}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Repos[0].Revision != first {
		t.Errorf("revision = %s, want %s", m.Repos[0].Revision, first)
	}
}

func TestPluginRepoMismatchFails(t *testing.T) {
	ws, _, _ := setup(t)
	c, _ := Load(ws)
	c.Repos[0].Name = "other"
	c.Schema = nil
	_, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), `facts repo is "shop-app", want "other"`) {
		t.Fatalf("got %v", err)
	}
}

func TestExpandDropsEmptyOptionalFlag(t *testing.T) {
	got := expand([]string{"x", "--root", "{root}", "--schema-facts", "{schema_facts}", "--keep"}, map[string]string{"root": "/r", "schema_facts": ""})
	if strings.Join(got, " ") != "x --root /r --keep" {
		t.Errorf("got %v", got)
	}
}

func TestLoadRejectsUnknownKeysAndPlugins(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, FileName), "name = \"x\"\nbogus = 1\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown key: %v", err)
	}
	write(t, filepath.Join(dir, FileName), "name = \"x\"\n[[repos]]\nname = \"a\"\npath = \"a\"\nplugins = [\"nope\"]\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), `unknown plugin "nope"`) {
		t.Errorf("unknown plugin: %v", err)
	}
}

func TestTemplateLoads(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, FileName), Template)
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFetchClonesMissingCheckout(t *testing.T) {
	ws, repo, _ := setup(t)
	c, _ := Load(ws)
	c.Schema = nil
	c.Repos[0].URL = repo // a local path works as a clone URL
	c.Repos[0].Path = filepath.Join(t.TempDir(), "clone", "shop-app")
	m, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: t.TempDir(), Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.Repos[0].Revision != run(t, repo, "git", "rev-parse", "HEAD") {
		t.Errorf("revision = %s", m.Repos[0].Revision)
	}
}
