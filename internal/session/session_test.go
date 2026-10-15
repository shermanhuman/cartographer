package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
)

func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", origin}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
}

// importAs imports the mini fixture under another workspace name and
// snapshot id, with its repos renamed.
func importAs(t *testing.T, s *store.Store, workspace, id, repoPrefix string) {
	t.Helper()
	src := "../../testdata/workspace-mini/snapshot"
	dst := t.TempDir()
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		txt := string(b)
		if repoPrefix != "" {
			txt = strings.ReplaceAll(txt, "shop-", repoPrefix)
		}
		if rel == "manifest.json" {
			txt = strings.Replace(txt, `"workspace": "mini"`, `"workspace": "`+workspace+`"`, 1)
			txt = strings.Replace(txt, "snapshot-20261015T120000Z", id, 1)
		}
		if repoPrefix != "" {
			rel = strings.ReplaceAll(rel, "shop-", repoPrefix)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		return os.WriteFile(filepath.Join(dst, rel), []byte(txt), 0o644)
	})
	if _, err := s.Import(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
}

func TestChooseNeverGuessesAcrossProjects(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	parent := t.TempDir()
	app := filepath.Join(parent, "anything-named")
	gitInit(t, app, "git@github.com:example/shop-app.git") // matched by origin URL
	unrelated := filepath.Join(parent, "unrelated")
	gitInit(t, unrelated, "https://github.com/example/unrelated")
	plain := t.TempDir()

	sess := &Session{Store: s, Cwd: app}
	if id, err := sess.choose(ctx); err != nil || id != "snapshot-20261015T120000Z" {
		t.Errorf("mapped repo: %q, %v", id, err)
	}
	if !sess.Mapped(ctx) {
		t.Error("app should be mapped")
	}
	sess.Cwd = unrelated
	var nm *ErrNotMapped
	if _, err := sess.choose(ctx); !errors.As(err, &nm) {
		t.Errorf("unrelated repo must not get another project's map: %v", err)
	}
	sess.Cwd = plain
	if id, err := sess.choose(ctx); err != nil || id == "" {
		t.Errorf("outside git with one workspace: %q, %v", id, err)
	}
	if sess.Mapped(ctx) {
		t.Error("a non-repo directory is not mapped")
	}

	// A second project: outside git it's now ambiguous; its own repo picks it.
	importAs(t, s, "other", "snapshot-20261016T000000Z", "acme-")
	if _, err := sess.choose(ctx); !errors.As(err, &nm) || !strings.Contains(err.Error(), "mini") {
		t.Errorf("outside git with two workspaces: %v", err)
	}
	acme := filepath.Join(parent, "acme-app")
	gitInit(t, acme, "https://github.com/example/acme-app")
	sess.Cwd = acme
	if id, _ := sess.choose(ctx); id != "snapshot-20261016T000000Z" {
		t.Errorf("acme repo chose %q", id)
	}
	sess.Cwd = app
	if id, _ := sess.choose(ctx); id != "snapshot-20261015T120000Z" {
		t.Errorf("shop repo chose %q (newest overall must not win)", id)
	}
}

// countGit puts a git wrapper first on PATH that logs each exec; the
// returned function reports how many ran so far.
func countGit(t *testing.T) func() int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "\n")
	}
}

func TestQueryDiscoversReposOncePerBurst(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	parent := t.TempDir()
	app := filepath.Join(parent, "shop-app")
	gitInit(t, app, "https://github.com/example/shop-app")
	gitInit(t, filepath.Join(parent, "shop-worker"), "https://github.com/example/shop-worker")
	for _, n := range []string{"aa-unrelated", "bb-unrelated", "zz-unrelated"} {
		gitInit(t, filepath.Join(parent, n), "https://github.com/example/"+n)
	}
	execs := countGit(t)

	sess := &Session{Store: s, Cwd: app}
	q, rep, err := sess.Query(ctx)
	if err != nil || q.Snapshot != "snapshot-20261015T120000Z" {
		t.Fatalf("query: %v", err)
	}
	if real, _ := filepath.EvalSymlinks(parent); rep.Repos[1].Path != filepath.Join(real, "shop-worker") {
		t.Errorf("worker not located: %+v", rep.Repos[1])
	}
	// rev-parse + origin of cwd, origin of shop-worker (named like a repo,
	// so scanned first; the unrelated siblings never are), then one rev-list
	// per repo (the fixture revisions aren't in these clones).
	if n := execs(); n != 5 {
		t.Errorf("first query ran %d git execs, want 5", n)
	}
	for range 3 {
		if _, _, err := sess.Query(ctx); err != nil {
			t.Fatal(err)
		}
		sess.Choose(ctx)
		sess.Fresh(ctx, "snapshot-20261015T120000Z")
	}
	if n := execs(); n != 5 {
		t.Errorf("cached queries ran git: %d execs total", n)
	}
	sess.Cwd = filepath.Join(parent, "shop-worker") // a new directory is resolved again
	if _, _, err := sess.Query(ctx); err != nil {
		t.Fatal(err)
	}
	if n := execs(); n <= 5 {
		t.Errorf("cwd change didn't refresh (%d execs)", n)
	}
}

func TestMappedIsCheap(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	parent := t.TempDir()
	unrelated := filepath.Join(parent, "unrelated")
	gitInit(t, unrelated, "https://github.com/example/unrelated")
	for i := range 10 {
		gitInit(t, filepath.Join(parent, fmt.Sprint("sib", i)), "https://github.com/example/x")
	}
	execs := countGit(t)
	if (&Session{Store: s, Cwd: unrelated}).Mapped(ctx) {
		t.Error("unrelated repo is not mapped")
	}
	if n := execs(); n != 2 {
		t.Errorf("Mapped ran %d git execs, want 2", n)
	}
}

// fakeGitHub serves the releases API; hang makes every request wait for the
// client to give up.
func fakeGitHub(t *testing.T, hang bool) (*snapshot.GitHub, *atomic.Int32, *atomic.Int32) {
	var hits, maxInFlight, inFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		if hang {
			<-r.Context().Done()
			return
		}
		time.Sleep(50 * time.Millisecond)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return &snapshot.GitHub{API: srv.URL, HTTP: srv.Client()}, &hits, &maxInFlight
}

func withWorkspace(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("workspaces = [\"example/map\"]\n"), 0o644)
	t.Setenv("CARTOGRAPHER_CONFIG", cfg)
}

func TestFailedOrHungPullNeverFailsQueries(t *testing.T) {
	ctx := context.Background()
	withWorkspace(t)
	old := PullTimeout
	PullTimeout = 300 * time.Millisecond
	t.Cleanup(func() { PullTimeout = old })
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	plain := t.TempDir()

	for _, hang := range []bool{false, true} {
		gh, _, _ := fakeGitHub(t, hang)
		var log strings.Builder
		sess := &Session{Store: s, DataDir: t.TempDir(), Cwd: plain, AutoPull: true, GitHub: gh, Log: &log}
		start := time.Now()
		for range 2 {
			q, _, err := sess.Query(ctx)
			if err != nil || q.Snapshot != "snapshot-20261015T120000Z" {
				t.Fatalf("hang=%v: query failed: %v", hang, err)
			}
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("hang=%v: queries took %v; the pull must be bounded", hang, d)
		}
		if got := strings.Count(log.String(), "auto-pull example/map failed"); got != 1 {
			t.Errorf("hang=%v: want one warning, got %q", hang, log.String())
		}
	}
}

func TestConcurrentQueriesPullOnce(t *testing.T) {
	ctx := context.Background()
	withWorkspace(t)
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	parent := t.TempDir()
	app := filepath.Join(parent, "shop-app")
	gitInit(t, app, "https://github.com/example/shop-app")
	gh, hits, maxInFlight := fakeGitHub(t, false)
	sess := &Session{Store: s, DataDir: t.TempDir(), Cwd: app, AutoPull: true, GitHub: gh, Log: io.Discard}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := sess.Query(ctx); err != nil {
				t.Error(err)
			}
			sess.Choose(ctx)
		}()
	}
	wg.Wait()
	if hits.Load() == 0 || maxInFlight.Load() != 1 {
		t.Errorf("pulls: %d requests, %d at once; want one at a time", hits.Load(), maxInFlight.Load())
	}
}

func TestPullerCreatesOneClient(t *testing.T) {
	t.Setenv("GH_TOKEN", "x") // no `gh` exec
	sess := &Session{}
	var wg sync.WaitGroup
	clients := make([]*snapshot.GitHub, 8)
	for i := range clients {
		wg.Add(1)
		go func() { defer wg.Done(); clients[i] = sess.Puller().GitHub }()
	}
	wg.Wait()
	for _, c := range clients {
		if c != clients[0] {
			t.Fatal("Puller created more than one GitHub client")
		}
	}
}
