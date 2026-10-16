package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
)

func sessionNoCache(ctx context.Context) context.Context { return session.WithoutCache(ctx) }

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
	// A directory without cartographer.toml is not a source of either kind.
	var out bytes.Buffer
	if err := run(context.Background(), []string{"workspace", "add", t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Errorf("plain directory: %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "already") && !strings.Contains(err.Error(), "running since") {
		t.Errorf("under a lock: %v", err)
	}
}

// A query from a checkout ahead of the snapshot spawns a detached
// `build --local --import` (here a script that records its arguments) and
// returns; the off switches stop it.
func TestQuerySpawnsADetachedRebuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script child")
	}
	root, repo, ws := localWorkspace(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	marker := filepath.Join(root, "child-args")
	script := filepath.Join(root, "fake-cartographer")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > '"+marker+"'\necho built > /dev/stdout\n"), 0o755)
	old := selfExe
	selfExe = func() (string, error) { return script, nil }
	t.Cleanup(func() { selfExe = old })
	t.Setenv("CARTOGRAPHER_NO_REBUILD", "")
	t.Chdir(repo)

	for _, off := range [][]string{{"--no-rebuild"}, nil} {
		os.Remove(marker)
		if off == nil {
			t.Setenv("CARTOGRAPHER_NO_REBUILD", "1")
		}
		cli(t, db, append([]string{"status", "--no-pull"}, off...)...)
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("rebuild spawned despite %v", append(off, "env"))
		}
	}
	t.Setenv("CARTOGRAPHER_NO_REBUILD", "")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("auto_rebuild = false\n"), 0o644)
	t.Setenv("CARTOGRAPHER_CONFIG", cfg)
	cli(t, db, "status", "--no-pull")
	if _, err := os.Stat(marker); err == nil {
		t.Error("rebuild spawned despite auto_rebuild = false")
	}
	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "none.toml"))

	st := cli(t, db, "status", "--no-pull")
	if !strings.Contains(st, "1 commits behind") {
		t.Fatalf("status:\n%s", st)
	}
	var args string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(marker); err == nil {
			args = strings.TrimSpace(string(b))
			break
		}
	}
	real, _ := filepath.EvalSymlinks(ws)
	if want := "build --local --import --workspace " + real + " --db " + db; args != want {
		t.Errorf("child args = %q, want %q", args, want)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(d.LogPath("mini")); strings.Contains(string(b), "built") {
			return
		}
	}
	t.Error("the child's output did not reach the rebuild log")
}

// The long-lived path: a query starts the build in-process, the query
// returns at once, and a later one answers from the new snapshot.
func TestBackgroundRebuildImportsANewSnapshot(t *testing.T) {
	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	_, repo, ws := localWorkspace(t)
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
	sess := newSession(c, s, &log)
	sess.Background = true
	sess.Rebuild = backgroundRebuild(c, sess, s)
	ctx := context.Background()
	start := time.Now()
	_, rep, err := sess.Query(ctx)
	if err != nil || rep.Repos[0].Behind != 1 {
		t.Fatalf("first query: %v, %+v", err, rep.Repos)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the query waited %v for the rebuild", d)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, running := d.Running("mini"); !running && d.Status("mini") == "" {
			// Snapshot ids have second granularity: a rebuild within the
			// first build's second replaces it under the same id.
			if _, rep, err := sess.Query(sessionNoCache(ctx)); err == nil && rep.Repos[0].Revision == head {
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
	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	_, repo, ws := localWorkspace(t)
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
	sess := newSession(c, s, &log)
	sess.Background = true
	sess.Rebuild = backgroundRebuild(c, sess, s)
	ctx := context.Background()
	if _, _, err := sess.Query(ctx); err != nil {
		t.Fatal(err)
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st := d.Status("mini"); strings.HasPrefix(st, "last rebuild failed") {
			_, rep, _ := sess.Query(sessionNoCache(ctx))
			if !strings.Contains(rep.Summary(), "last rebuild failed") || !strings.Contains(st, "plugin replay") || !strings.Contains(log.String(), "local rebuild of mini failed") {
				t.Errorf("status %q; summary %q; log %q", st, rep.Summary(), log.String())
			}
			if _, _, err := sess.Query(sessionNoCache(ctx)); err != nil || d.Due("mini") {
				t.Error("a failed rebuild must back off")
			}
			return
		}
	}
	t.Fatalf("no failure recorded; log %s", log.String())
}
