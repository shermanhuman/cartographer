package workspace

import (
	"context"
	"fmt"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/store"
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

func TestReviewStampsFingerprints(t *testing.T) {
	ws, _, _ := setup(t)
	c, _ := Load(ws)
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sid, err := s.Import(context.Background(), "../../testdata/workspace-mini/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	n, err := Review(context.Background(), c, s.DB, sid, "checkout")
	if err != nil || n != 3 {
		t.Fatalf("Review = %d, %v", n, err)
	}
	b := mustRead(t, filepath.Join(ws, "annotations/journeys.json"))
	for _, want := range []string{`"shop-app:Shop.Orders.create/2": "h2-changed"`, `"shop-app:Shop.Billing.charge/2": "h3"`, `"outcome": "An order row exists."`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
	if _, err := Review(context.Background(), c, s.DB, sid, "nope"); err == nil {
		t.Error("unknown annotation should fail")
	}
}

// A filtered (blobless) clone from a remote that honours --filter: git
// archive must lazily fetch blobs through the same environment.
func TestBuildFetchPartialCloneFromFilteredRemote(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok-partial")
	ws, repo, _ := setup(t)
	// A PR head that the default refspec doesn't fetch.
	run(t, repo, "git", "checkout", "-qb", "pr")
	write(t, filepath.Join(repo, "lib/shop/billing.ex"), "defmodule Shop.Billing do end\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "pr")
	head := run(t, repo, "git", "rev-parse", "HEAD")
	run(t, repo, "git", "checkout", "-q", "main")
	bare := filepath.Join(t.TempDir(), "remote.git")
	run(t, repo, "git", "clone", "-q", "--bare", repo, bare)
	run(t, bare, "git", "update-ref", "refs/pull/1/head", head)
	run(t, bare, "git", "branch", "-D", "pr")
	run(t, bare, "git", "config", "uploadpack.allowFilter", "true")
	run(t, bare, "git", "config", "uploadpack.allowAnySHA1InWant", "true")

	c, _ := Load(ws)
	c.Schema = nil
	c.Repos[0].URL = "file://" + bare
	clone := filepath.Join(t.TempDir(), "clone", "shop-app")
	c.Repos[0].Path = clone
	m, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: t.TempDir(), Fetch: true, Refs: map[string]string{"shop-app": head}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Repos[0].Revision != head {
		t.Errorf("revision = %s, want %s", m.Repos[0].Revision, head)
	}
	if got := run(t, clone, "git", "config", "remote.origin.partialclonefilter"); got != "blob:none" {
		t.Errorf("clone is not partial (filter %q): the test would not exercise lazy fetch", got)
	}
}

func TestGitEnvCarriesAuthOffArgv(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok-env")
	t.Setenv("GIT_CONFIG_COUNT", "")
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	got, err := git(context.Background(), dir, "config", "--get", extraHeaderKey)
	if err != nil {
		t.Fatal(err)
	}
	want := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:tok-env"))
	if strings.TrimSpace(got) != want {
		t.Errorf("extraheader = %q, want %q", got, want)
	}
}

func TestFetchErrorDoesNotLeakToken(t *testing.T) {
	const tok = "ghs_supersecret123"
	t.Setenv("GH_TOKEN", tok)
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	r := Repo{Name: "x", URL: "file://" + filepath.Join(t.TempDir(), "missing.git")}
	err := fetchRepo(context.Background(), r, filepath.Join(t.TempDir(), "x"), "")
	if err == nil {
		t.Fatal("fetch of a missing remote should fail")
	}
	for _, s := range []string{tok, basic} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks credential: %v", err)
		}
	}
	got := redact("fatal: bad config http.https://github.com/.extraheader=AUTHORIZATION: basic " + basic + "\nnext " + tok)
	if strings.Contains(got, basic) || strings.Contains(got, tok) || !strings.Contains(got, "next") {
		t.Errorf("redact = %q", got)
	}
}

func TestPluginsAndPrepareDoNotSeeTokens(t *testing.T) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(k, "secret-"+k)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraheader")
	t.Setenv("GIT_CONFIG_VALUE_0", "secret-header")
	ws, _, _ := setup(t)
	c, _ := Load(ws)
	envOut := filepath.Join(t.TempDir(), "env")
	p := c.Plugins["fake"]
	p.Command = []string{"sh", "-c", "env >> " + envOut + " && " + p.Command[2], "plugin", "{root}"}
	c.Plugins["fake"] = p
	c.Schema.Prepare = "env >> " + envOut
	if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	env := mustRead(t, envOut)
	if !strings.Contains(env, "SF=set") {
		t.Fatalf("plugin env not captured:\n%s", env)
	}
	if strings.Contains(env, "secret") || strings.Contains(env, "GIT_CONFIG_") {
		t.Errorf("child env carries credentials:\n%s", env)
	}
}

func TestSchemaCacheRebuildsInvalidEntryAndKeysOnEnv(t *testing.T) {
	ws, _, log := setup(t)
	c, _ := Load(ws)
	cache := t.TempDir()
	build := func() {
		t.Helper()
		if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: cache}); err != nil {
			t.Fatal(err)
		}
	}
	build()
	entries, _ := filepath.Glob(filepath.Join(cache, "schema", "*.json"))
	if len(entries) != 1 {
		t.Fatalf("cache entries = %v", entries)
	}
	// A truncated entry is rebuilt, not trusted.
	os.WriteFile(entries[0], []byte(`{"facts_version":`), 0o644)
	build()
	if n := strings.Count(mustRead(t, log), "prepared"); n != 2 {
		t.Errorf("prepare ran %d times, want 2 (invalid cache rebuilt)", n)
	}
	build()
	if n := strings.Count(mustRead(t, log), "prepared"); n != 2 {
		t.Errorf("prepare ran %d times, want 2 (rebuilt entry reused)", n)
	}
	// Changing schema env or the plugin code misses the cache.
	c.Schema.Env = map[string]string{"MIX_ENV": "test"}
	build()
	if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "s"), CacheDir: cache, PluginsDigest: "other"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(mustRead(t, log), "prepared"); n != 4 {
		t.Errorf("prepare ran %d times, want 4 (env and plugins in key)", n)
	}
	if tmps, _ := filepath.Glob(filepath.Join(cache, "schema", ".*")); len(tmps) != 0 {
		t.Errorf("temp files left: %v", tmps)
	}
}

func TestLoadValidatesKeepAndPrepareRepo(t *testing.T) {
	dir := t.TempDir()
	for _, keep := range []string{"0", "-1"} {
		write(t, filepath.Join(dir, FileName), "name = \"x\"\nkeep = "+keep+"\n")
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "keep must be at least 1") {
			t.Errorf("keep=%s: %v", keep, err)
		}
	}
	write(t, filepath.Join(dir, FileName), "name = \"x\"\n")
	if c, err := Load(dir); err != nil || c.Keep != 30 {
		t.Errorf("default keep: %v %v", c, err)
	}
	write(t, filepath.Join(dir, FileName), "name = \"x\"\n[schema]\nprepare = \"true\"\ncommand = [\"cat\"]\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "prepare_repo") {
		t.Errorf("prepare without prepare_repo: %v", err)
	}
}

// An extraction error with git archive output still unread must fail fast,
// not block on the pipe (the old code waited on git before draining it).
func TestArchiveErrorDoesNotHang(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	big := strings.Repeat("x", 4096)
	for i := 0; i < 200; i++ { // ~800 KB of archive, far beyond a pipe buffer
		write(t, filepath.Join(repo, fmt.Sprintf("f%03d.txt", i)), big+fmt.Sprint(i))
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "big")
	blocker := filepath.Join(t.TempDir(), "file")
	write(t, blocker, "not a directory")
	done := make(chan error, 1)
	go func() {
		_, err := archive(context.Background(), repo, "HEAD", filepath.Join(blocker, "dst"))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error extracting under a regular file")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("archive hung after an extraction error")
	}
}
