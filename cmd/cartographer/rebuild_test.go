package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
)

// syncBuf is a session log the test can read while the rebuild writes it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// localWorkspace makes a repo "shop-app" with a commit and, beside it, a
// workspace "mini" whose plugin replays the fixture facts. The repo's
// configured ref is origin/main, which a checkout without a remote lacks:
// only --local (HEAD) can build it.
func localWorkspace(t *testing.T) (root, repo, ws string) {
	t.Helper()
	root = t.TempDir()
	repo = filepath.Join(root, "shop-app")
	os.MkdirAll(filepath.Join(repo, "lib/shop"), 0o755)
	os.WriteFile(filepath.Join(repo, "lib/shop/billing.ex"), []byte("defmodule Shop.Billing do end\n"), 0o644)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "init")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot/facts/shop-app.json")
	ws = filepath.Join(root, "map")
	os.MkdirAll(ws, 0o755)
	gitIn(t, ws, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), []byte(`
name = "mini"
[[repos]]
name = "shop-app"
url = "https://github.com/example/shop-app"
path = "../shop-app"
ref = "origin/main"
plugins = ["replay"]
[plugins.replay]
command = ["cat", "`+fixture+`"]
`), 0o644)
	return root, repo, ws
}

// register points the user config at a fresh file listing ws (and extra
// lines).
func register(t *testing.T, ws string, extra ...string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	body := "local_workspaces = [" + strconv.Quote(ws) + "]\n" + strings.Join(extra, "\n") + "\n"
	os.WriteFile(cfg, []byte(body), 0o644)
	t.Setenv("CARTOGRAPHER_CONFIG", cfg)
}

func appendToml(t *testing.T, ws, text string) {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(ws, "cartographer.toml"))
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), append(b, []byte(text)...), 0o644)
}

// realChild makes spawned rebuilds run this test binary as the CLI (see
// TestMain), through a script that counts the spawns.
func realChild(t *testing.T) (spawns func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script child")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := filepath.Join(dir, "cartographer")
	os.WriteFile(script, []byte("#!/bin/sh\necho x >> '"+count+"'\nexec '"+self+"' \"$@\"\n"), 0o755)
	old := selfExe
	selfExe = func() (string, error) { return script, nil }
	t.Cleanup(func() { selfExe = old })
	t.Setenv("CARTOGRAPHER_TEST_CLI", "1")
	t.Setenv("CARTOGRAPHER_NO_REBUILD", "")
	return func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "\n")
	}
}

// settle waits for a spawned child to finish: a stamp written after since
// and no lock.
func settle(t *testing.T, d rebuild.Dir, ws string, since time.Time) {
	t.Helper()
	stamp := filepath.Join(d.Path, "rebuild", ws+".stamp")
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		fi, err := os.Stat(stamp)
		if _, running := d.Running(ws); err == nil && !fi.ModTime().Before(since) && !running {
			return
		}
	}
	b, _ := os.ReadFile(d.LogPath(ws))
	t.Fatalf("the rebuild child did not finish; log:\n%s", b)
}

func TestWorkspaceAddRecordsALocalDirectory(t *testing.T) {
	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	_, _, ws := localWorkspace(t)
	if out := cli(t, "", "workspace", "add", ws); !strings.Contains(out, "added local workspace "+ws+" (mini)") {
		t.Error(out)
	}
	cli(t, "", "workspace", "add", "acme/map") // owner/name still pulls
	cfg, err := userconfig.Load()
	if err != nil || len(cfg.LocalWorkspaces) != 1 || cfg.LocalWorkspaces[0] != ws || len(cfg.Workspaces) != 1 {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	// A directory without cartographer.toml is not a source of either kind;
	// one whose cartographer.toml has no name can never match a snapshot.
	var out bytes.Buffer
	if err := run(context.Background(), []string{"workspace", "add", t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Errorf("plain directory: %v", err)
	}
	nameless := t.TempDir()
	os.WriteFile(filepath.Join(nameless, "cartographer.toml"), []byte("keep = 3\n"), 0o644)
	if err := run(context.Background(), []string{"workspace", "add", nameless}, &out); err == nil || !strings.Contains(err.Error(), "has no `name`") {
		t.Errorf("nameless workspace: %v", err)
	}
}

// build --local maps the checkout's HEAD, ignoring the configured ref, and
// leaves no snapshot directory behind when importing; status is current.
func TestBuildLocalImportsCheckoutHeads(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"build", "--workspace", ws, "--db", db}, &out); err == nil {
		t.Fatal("origin/main built without a remote")
	}
	out.Reset()
	if err := run(context.Background(), []string{"build", "--local", "--import", "--workspace", ws, "--db", db}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "imported snapshot-") {
		t.Fatal(out.String())
	}
	if m, _ := filepath.Glob(filepath.Join(ws, ".cartographer", "snapshots", "build-*")); len(m) != 0 {
		t.Errorf("a --local --import build left its snapshot in the workspace: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(db), "rebuild", "mini.build-*")); len(m) != 0 {
		t.Errorf("scratch left behind: %v", m)
	}
	head := gitIn(t, repo, "rev-parse", "HEAD")
	t.Chdir(repo)
	if st := cli(t, db, "status", "--no-pull"); !strings.Contains(st, "shop-app@"+head[:7]+": current") || strings.Contains(st, "rebuild") {
		t.Errorf("status:\n%s", st)
	}
	if err := run(context.Background(), []string{"build", "--local", "--fetch", "--workspace", ws, "--db", db}, &out); err == nil {
		t.Error("--local with --fetch accepted")
	}
	// The lock is honoured: a build while one runs fails without building.
	d := rebuild.Dir{Path: filepath.Dir(db)}
	unlock, _ := d.Lock("mini")
	defer unlock()
	err := run(context.Background(), []string{"build", "--local", "--import", "--workspace", ws, "--db", db}, &out)
	if !errors.Is(err, rebuild.ErrRunning) || !strings.Contains(err.Error(), "running since") {
		t.Errorf("under a lock: %v", err)
	}
}

// Everything a local build does happens under the lock, so a failure
// before the build proper (a checkout not cloned) is stamped: it backs off
// and status reports it; --background writes it to the log.
func TestBuildLocalRecordsSetupFailures(t *testing.T) {
	_, _, ws := localWorkspace(t)
	appendToml(t, ws, "[[repos]]\nname = \"missing-app\"\npath = \"../missing-app\"\n")
	db := filepath.Join(t.TempDir(), "carto.db")
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for _, bg := range []bool{false, true} {
		args := []string{"build", "--local", "--import", "--workspace", ws, "--db", db}
		if bg {
			args = append(args, "--background")
		}
		var out bytes.Buffer
		err := run(context.Background(), args, &out)
		if err == nil || !strings.Contains(err.Error(), "no checkout for missing-app") {
			t.Fatalf("background=%v: %v", bg, err)
		}
		if d.Due("mini") || !strings.Contains(d.Status("mini"), "last rebuild failed") || !strings.Contains(d.Status("mini"), "missing-app") {
			t.Errorf("background=%v: due %v, status %q", bg, d.Due("mini"), d.Status("mini"))
		}
	}
	if b, _ := os.ReadFile(d.LogPath("mini")); !strings.Contains(string(b), "no checkout for missing-app") {
		t.Errorf("log:\n%s", b)
	}
}

// A query from a checkout ahead of the snapshot spawns a detached
// `build --local --import --background` (the real one) and returns; a later
// query answers from the new snapshot. The off switches stop it, and an
// unregistered workspace is never built.
func TestQuerySpawnsADetachedRebuild(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	spawns := realChild(t)
	t.Chdir(repo)

	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	if st := cli(t, db, "status", "--no-pull"); spawns() != 0 || !strings.Contains(st, "cartographer workspace add") {
		t.Errorf("unregistered: %d spawns, status:\n%s", spawns(), st)
	}
	register(t, ws)
	cli(t, db, "status", "--no-pull", "--no-rebuild")
	t.Setenv("CARTOGRAPHER_NO_REBUILD", "1")
	cli(t, db, "status", "--no-pull")
	t.Setenv("CARTOGRAPHER_NO_REBUILD", "")
	register(t, ws, "auto_rebuild = false")
	cli(t, db, "status", "--no-pull")
	if spawns() != 0 {
		t.Fatalf("%d rebuilds spawned despite the off switches", spawns())
	}

	register(t, ws)
	d := rebuild.Dir{Path: filepath.Dir(db)}
	start := time.Now()
	if st := cli(t, db, "status", "--no-pull"); !strings.Contains(st, "1 commits behind") {
		t.Fatalf("status:\n%s", st)
	}
	settle(t, d, "mini", start)
	st := cli(t, db, "status", "--no-pull")
	if !strings.Contains(st, "shop-app@"+head[:7]+": current") || strings.Contains(st, "rebuild") || spawns() != 1 {
		t.Errorf("after the rebuild (%d spawns):\n%s", spawns(), st)
	}
	if b, _ := os.ReadFile(d.LogPath("mini")); !strings.Contains(string(b), "imported snapshot-") {
		t.Errorf("log:\n%s", b)
	}
}

// A workspace that can't be built (a repo not cloned) fails in the child:
// repeated queries spawn one child per FailBackoff, and status says why.
func TestFailingRebuildSpawnsOncePerBackoff(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	appendToml(t, ws, "[[repos]]\nname = \"missing-app\"\npath = \"../missing-app\"\n")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	spawns := realChild(t)
	register(t, ws)
	t.Chdir(repo)
	d := rebuild.Dir{Path: filepath.Dir(db)}
	start := time.Now()
	cli(t, db, "status", "--no-pull")
	settle(t, d, "mini", start)
	for range 3 {
		cli(t, db, "status", "--no-pull")
		time.Sleep(100 * time.Millisecond)
	}
	st := cli(t, db, "status", "--no-pull")
	if spawns() != 1 || !strings.Contains(st, "last rebuild failed") || !strings.Contains(st, "missing-app") {
		t.Errorf("%d spawns; status:\n%s", spawns(), st)
	}
}

// A child that cannot start is reported once and recorded as a failure.
func TestSpawnFailureIsRecorded(t *testing.T) {
	_, _, ws := localWorkspace(t)
	old := selfExe
	selfExe = func() (string, error) { return filepath.Join(t.TempDir(), "no-such-binary"), nil }
	t.Cleanup(func() { selfExe = old })
	db := filepath.Join(t.TempDir(), "carto.db")
	var log bytes.Buffer
	spawnRebuild(&common{db: db}, &log)("mini", ws)
	d := rebuild.Dir{Path: filepath.Dir(db)}
	if !strings.Contains(log.String(), "could not start the rebuild") || d.Due("mini") || !strings.Contains(d.Status("mini"), "could not start the rebuild") {
		t.Errorf("log %q, due %v, status %q", log.String(), d.Due("mini"), d.Status("mini"))
	}
}

// The long-lived path: a query starts the build in-process, the query
// returns at once, and a later one answers from the new snapshot.
func TestBackgroundRebuildImportsANewSnapshot(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	register(t, ws)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &common{db: db}
	t.Chdir(repo)
	var log syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	sess := newSession(c, s, &log)
	sess.Background = true
	start, wait := backgroundRebuild(ctx, c, sess, s)
	sess.Rebuild = start
	defer func() { cancel(); wait() }()
	begun := time.Now()
	_, rep, err := sess.Query(ctx)
	if err != nil || rep.Repos[0].Behind != 1 {
		t.Fatalf("first query: %v, %+v", err, rep.Repos)
	}
	if d := time.Since(begun); d > 2*time.Second {
		t.Errorf("the query waited %v for the rebuild", d)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, running := d.Running("mini"); !running && d.Status("mini") == "" {
			// Snapshot ids have second granularity: a rebuild within the
			// first build's second replaces it under the same id.
			if _, rep, err := sess.Query(session.WithoutCache(ctx)); err == nil && rep.Repos[0].Revision == head {
				if rep.Repos[0].Behind != 0 {
					t.Errorf("after the rebuild: %+v", rep.Repos[0])
				}
				if log.String() != "" {
					t.Errorf("warnings: %s", log.String())
				}
				return
			}
		}
	}
	b, _ := os.ReadFile(d.LogPath("mini"))
	t.Fatalf("no new snapshot; status %q; log %s; build log %s", d.Status("mini"), log.String(), b)
}

// A failing rebuild is reported once in the session log and in status.
func TestBackgroundRebuildFailureIsVisible(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	register(t, ws)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	toml, _ := os.ReadFile(filepath.Join(ws, "cartographer.toml"))
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), bytes.Replace(toml, []byte(`["cat", "`), []byte(`["false", "`), 1), 0o644)
	s, _ := store.Open(db)
	defer s.Close()
	c := &common{db: db}
	t.Chdir(repo)
	var log syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	sess := newSession(c, s, &log)
	sess.Background = true
	start, wait := backgroundRebuild(ctx, c, sess, s)
	sess.Rebuild = start
	defer func() { cancel(); wait() }()
	if _, _, err := sess.Query(ctx); err != nil {
		t.Fatal(err)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st := d.Status("mini"); strings.HasPrefix(st, "last rebuild failed") {
			_, rep, _ := sess.Query(session.WithoutCache(ctx))
			if !strings.Contains(rep.Summary(), "last rebuild failed") || !strings.Contains(st, "plugin replay") || !strings.Contains(log.String(), "local rebuild of mini failed") {
				t.Errorf("status %q; summary %q; log %q", st, rep.Summary(), log.String())
			}
			if _, _, err := sess.Query(session.WithoutCache(ctx)); err != nil || d.Due("mini") {
				t.Error("a failed rebuild must back off")
			}
			return
		}
	}
	t.Fatalf("no failure recorded; log %s", log.String())
}

// Shutting down mcp/serve cancels a running background rebuild: its lock
// and scratch go, and the cancellation is not recorded as a failure.
func TestShutdownCancelsABackgroundRebuild(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	register(t, ws)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	toml, _ := os.ReadFile(filepath.Join(ws, "cartographer.toml"))
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), bytes.Replace(toml, []byte(`["cat", "`), []byte(`["sh", "-c", "sleep 30", "`), 1), 0o644)
	s, _ := store.Open(db)
	defer s.Close()
	c := &common{db: db}
	t.Chdir(repo)
	ctx, cancel := context.WithCancel(context.Background())
	sess := newSession(c, s, nil)
	sess.Background = true
	start, wait := backgroundRebuild(ctx, c, sess, s)
	sess.Rebuild = start
	if _, _, err := sess.Query(ctx); err != nil {
		t.Fatal(err)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m, _ := filepath.Glob(filepath.Join(d.Path, "rebuild", "mini.build-*")); len(m) > 0 {
			break
		}
	}
	if _, running := d.Running("mini"); !running {
		t.Fatal("the rebuild did not start")
	}
	stopped := time.Now()
	cancel()
	wait()
	if el := time.Since(stopped); el > 8*time.Second {
		t.Errorf("shutdown took %v", el)
	}
	if _, running := d.Running("mini"); running {
		t.Error("lock left behind")
	}
	if m, _ := filepath.Glob(filepath.Join(d.Path, "rebuild", "mini.build-*")); len(m) != 0 {
		t.Errorf("scratch left behind: %v", m)
	}
	if !d.Due("mini") || d.Status("mini") != "" {
		t.Errorf("a cancelled rebuild was recorded: due %v, status %q", d.Due("mini"), d.Status("mini"))
	}
}
