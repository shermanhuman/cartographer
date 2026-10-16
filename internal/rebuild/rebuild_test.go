package rebuild

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func makeWorkspace(t *testing.T, dir, name string) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "cartographer.toml"), []byte("name = \""+name+"\"\n[[repos]]\nname = \"shop-app\"\npath = \"../shop-app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Only a registered directory (local_workspaces) is a workspace to
// rebuild from: a sibling claiming the name is never trusted for that.
func TestRegisteredWorkspace(t *testing.T) {
	configured := makeWorkspace(t, filepath.Join(t.TempDir(), "elsewhere"), "mini")
	other := makeWorkspace(t, filepath.Join(t.TempDir(), "other"), "other")
	if got := Registered("mini", []string{filepath.Join(t.TempDir(), "missing"), other, configured}); got != configured {
		t.Errorf("Registered = %q, want %q", got, configured)
	}
	if got := Registered("mini", nil); got != "" {
		t.Errorf("nothing registered: %q", got)
	}
	home, _ := os.UserHomeDir()
	if rel, err := filepath.Rel(home, configured); err == nil && !strings.HasPrefix(rel, "..") {
		if got := Registered("mini", []string{"~/" + rel}); got != configured {
			t.Errorf("~ path: %q", got)
		}
	}
}

// A sibling workspace with the snapshot's name is only suggested.
func TestSiblingWorkspace(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	app := filepath.Join(parent, "shop-app")
	gitInit(t, app)
	sibling := makeWorkspace(t, filepath.Join(parent, "shop-map"), "mini")
	makeWorkspace(t, filepath.Join(parent, "other-map"), "other")
	os.MkdirAll(filepath.Join(parent, "no-config"), 0o755)
	cur := fresh.Find(ctx, app)
	if got := Sibling("mini", cur); !samePath(got, sibling) {
		t.Errorf("sibling: got %q, want %q", got, sibling)
	}
	if got := Sibling("third", cur); got != "" {
		t.Errorf("unknown name found %q", got)
	}
	if got := Sibling("mini", fresh.Checkout{}); got != "" {
		t.Errorf("outside git: %q", got)
	}
}

func samePath(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return ra == rb && a != ""
}

func TestNeeded(t *testing.T) {
	for _, tc := range []struct {
		st   fresh.State
		want bool
	}{
		{fresh.State{Behind: 0}, false},
		{fresh.State{Behind: 2}, true},
		{fresh.State{Behind: -1, Note: fresh.NoteRevisionMissing}, true},
		{fresh.State{Behind: -1, Note: "no local checkout found"}, false},
		{fresh.State{Behind: 0, Changed: map[string]bool{"a.ex": true}}, false}, // uncommitted edits stay ⚠
	} {
		rep := &fresh.Report{Repos: []fresh.State{{Behind: 0}, tc.st}}
		if got := Needed(rep); got != tc.want {
			t.Errorf("%+v: Needed = %v", tc.st, got)
		}
	}
	if Needed(nil) {
		t.Error("nil report")
	}
}

// The lock is exclusive across processes (O_EXCL), and a lock older than
// Stale is taken over.
func TestLockIsExclusiveAndStaleLocksAreTakenOver(t *testing.T) {
	d := Dir{t.TempDir()}
	unlock, err := d.Lock("mini")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lock("mini"); !errors.Is(err, ErrRunning) {
		t.Errorf("second lock: %v", err)
	}
	if since, ok := d.Running("mini"); !ok || time.Since(since) > time.Minute {
		t.Errorf("Running = %v, %v", since, ok)
	}
	if _, err := d.Lock("other"); err != nil { // another workspace is independent
		t.Errorf("other workspace: %v", err)
	}
	unlock()
	if _, ok := d.Running("mini"); ok {
		t.Error("still running after unlock")
	}
	// A lock older than Stale, even from a live process.
	os.WriteFile(d.lockPath("mini"), []byte(fmt.Sprintf("%d 2000-01-01T00:00:00Z\n", os.Getpid())), 0o644)
	old := time.Now().Add(-Stale - time.Minute)
	os.Chtimes(d.lockPath("mini"), old, old)
	if _, ok := d.Running("mini"); ok {
		t.Error("a stale lock counts as running")
	}
	if _, err := d.Lock("mini"); err != nil {
		t.Errorf("stale lock not taken over: %v", err)
	}
}

// deadPid returns the pid of a process that has exited.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if runtime.GOOS == "windows" {
		t.Skip("no kill(pid, 0)")
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A fresh lock whose process is gone (killed with its MCP server) is stale
// at once, not after Stale.
func TestLockOfADeadProcessIsTakenOver(t *testing.T) {
	d := Dir{t.TempDir()}
	os.MkdirAll(filepath.Dir(d.lockPath("mini")), 0o755)
	os.WriteFile(d.lockPath("mini"), []byte(fmt.Sprintf("%d %s\n", deadPid(t), time.Now().UTC().Format(time.RFC3339))), 0o644)
	if _, ok := d.Running("mini"); ok {
		t.Error("a dead process's lock counts as running")
	}
	if st := d.Status("mini"); st != "" {
		t.Errorf("Status = %q", st)
	}
	unlock, err := d.Lock("mini")
	if err != nil {
		t.Fatalf("dead lock not taken over: %v", err)
	}
	defer unlock()
	if b, _ := os.ReadFile(d.lockPath("mini")); !strings.HasPrefix(string(b), fmt.Sprint(os.Getpid())+" ") {
		t.Errorf("lock holds %q", b)
	}
}

// Many processes taking over one stale lock at once: exactly one wins.
func TestStaleTakeoverIsAtomic(t *testing.T) {
	for range 20 {
		d := Dir{t.TempDir()}
		os.MkdirAll(filepath.Dir(d.lockPath("mini")), 0o755)
		os.WriteFile(d.lockPath("mini"), []byte(fmt.Sprintf("%d 2000-01-01T00:00:00Z\n", deadPid(t))), 0o644)
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := d.Lock("mini"); err == nil {
					wins.Add(1)
				} else if !errors.Is(err, ErrRunning) {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d processes took the stale lock", wins.Load())
		}
	}
}

// Run holds the lock around build and stamps the outcome: a failure backs
// off for FailBackoff and keeps the error for Status; a success clears it
// and records the revisions built.
func TestRunStampsAndBacksOff(t *testing.T) {
	d := Dir{t.TempDir()}
	if !d.Due("mini") {
		t.Fatal("a never-built workspace is due")
	}
	boom := errors.New("plugin elixir: exit status 1\nmore detail")
	if err := d.Run("mini", func() (Result, error) {
		os.WriteFile(d.LogPath("mini"), []byte("building…\n"), 0o644) // the caller writes the log
		if _, err := d.Lock("mini"); !errors.Is(err, ErrRunning) {
			t.Errorf("not locked during build: %v", err)
		}
		return Result{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Run = %v", err)
	}
	if d.Due("mini") {
		t.Error("due right after a failure")
	}
	st := d.Status("mini")
	if !strings.HasPrefix(st, "last rebuild failed ") || !strings.Contains(st, "plugin elixir: exit status 1") || strings.Contains(st, "more detail") || !strings.Contains(st, "(log: "+d.LogPath("mini")+")") {
		t.Errorf("Status after failure = %q", st)
	}
	// Backoff elapsed: due again; a success clears the error.
	old := time.Now().Add(-FailBackoff - time.Minute)
	os.Chtimes(d.stampPath("mini"), old, old)
	if !d.Due("mini") {
		t.Error("not due after the backoff")
	}
	revs := map[string]string{"shop-app": "aaaa", "shop-worker": "bbbb"}
	db := filepath.Join(d.Path, "carto.db")
	if err := d.Run("mini", func() (Result, error) { return Result{DB: db, Revisions: revs}, nil }); err != nil {
		t.Fatal(err)
	}
	if st := d.Status("mini"); st != "" {
		t.Errorf("Status after success = %q", st)
	}
	if got, at := d.Built("mini", db); !maps.Equal(got, revs) || time.Since(at) > time.Minute {
		t.Errorf("Built = %v, %v", got, at)
	}
	// Only an import into the same store counts as built.
	if got, _ := d.Built("mini", filepath.Join(d.Path, "other.db")); got != nil {
		t.Errorf("Built for another db = %v", got)
	}
	if !d.Due("mini") { // success never backs off: the next evidence triggers again
		t.Error("not due after a success")
	}
	// A held lock: Run reports it without building or stamping.
	unlock, _ := d.Lock("mini")
	ran := false
	if err := d.Run("mini", func() (Result, error) { ran = true; return Result{}, nil }); !errors.Is(err, ErrRunning) || ran {
		t.Errorf("Run under a lock: %v, ran %v", err, ran)
	}
	if st := d.Status("mini"); !strings.HasPrefix(st, "rebuilding since ") {
		t.Errorf("Status while locked = %q", st)
	}
	unlock()
	// A cancelled build (shutdown) is not a failure.
	if err := d.Run("mini", func() (Result, error) { return Result{}, context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got, _ := d.Built("mini", db); !d.Due("mini") || d.Status("mini") != "" || !maps.Equal(got, revs) {
		t.Errorf("a cancelled build was stamped: due %v, status %q", d.Due("mini"), d.Status("mini"))
	}
	// A rebuild that never started (spawn failed) backs off like a failure.
	d.Fail("mini", errors.New("start: exec format error"))
	if got, _ := d.Built("mini", db); d.Due("mini") || !strings.Contains(d.Status("mini"), "start: exec format error") || got != nil {
		t.Errorf("Fail: due %v, status %q", d.Due("mini"), d.Status("mini"))
	}
}

// Status cuts a long first line on a rune boundary.
func TestStatusCutsOnARuneBoundary(t *testing.T) {
	d := Dir{t.TempDir()}
	d.Fail("mini", errors.New(strings.Repeat("é", 200)))
	st := d.Status("mini")
	if !strings.Contains(st, "…") || !utf8Valid(st) {
		t.Errorf("Status = %q", st)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// Each workspace's state lives in its own directory, named safely and
// distinctly: no workspace's cleanup or lock touches another's.
func TestWorkspacesDoNotShareState(t *testing.T) {
	d := Dir{t.TempDir()}
	for _, pair := range [][2]string{{"shop", "shop.build-api"}, {"a/b", "a_b"}, {"my org/map", "my_org_map"}} {
		a, b := d.StateDir(pair[0]), d.StateDir(pair[1])
		if a == b || filepath.Dir(a) != filepath.Join(d.Path, "rebuild") || strings.ContainsAny(filepath.Base(a), "/ ") {
			t.Errorf("%q, %q -> %q, %q", pair[0], pair[1], a, b)
		}
	}
	unlockB, err := d.Lock("shop.build-api")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockB()
	d.Fail("shop.build-api", errors.New("boom"))
	os.WriteFile(d.LogPath("shop.build-api"), []byte("log"), 0o644)
	scratchB, _ := d.Scratch("shop.build-api")
	unlockA, err := d.Lock("shop")
	if err != nil {
		t.Fatalf("shop blocked by shop.build-api: %v", err)
	}
	defer unlockA()
	if _, err := d.Scratch("shop"); err != nil {
		t.Fatal(err)
	}
	if _, running := d.Running("shop.build-api"); !running {
		t.Error("shop's cleanup removed shop.build-api's lock")
	}
	if _, err := os.Stat(scratchB); err != nil {
		t.Error("shop's cleanup removed shop.build-api's scratch")
	}
	if !strings.Contains(d.Status("shop.build-api"), "rebuilding") || d.Due("shop.build-api") {
		t.Errorf("shop.build-api state lost: %q", d.Status("shop.build-api"))
	}
	if _, err := os.Stat(d.LogPath("shop.build-api")); err != nil {
		t.Error("log lost")
	}
	if !d.Due("shop") {
		t.Error("shop shares shop.build-api's stamp")
	}
}

// A rebuild whose lock was taken over (it outlived it) records nothing:
// its outcome would overwrite the newer owner's.
func TestLostLockIsNotStamped(t *testing.T) {
	d := Dir{t.TempDir()}
	err := d.Run("mini", func() (Result, error) {
		os.WriteFile(d.lockPath("mini"), []byte("1 2000-01-01T00:00:00Z other\n"), 0o644) // taken over
		return Result{}, errors.New("late failure")
	})
	if !errors.Is(err, ErrLost) {
		t.Errorf("Run = %v", err)
	}
	if _, err := os.Stat(d.stampPath("mini")); err == nil {
		t.Error("an old owner stamped its outcome")
	}
	if b, _ := os.ReadFile(d.lockPath("mini")); !strings.Contains(string(b), "other") {
		t.Error("an old owner removed the new owner's lock")
	}
}

// A live build keeps its lock fresh, so it never ages out while running.
func TestHeartbeatKeepsALiveLockFresh(t *testing.T) {
	old := Heartbeat
	Heartbeat = 20 * time.Millisecond
	t.Cleanup(func() { Heartbeat = old })
	d := Dir{t.TempDir()}
	err := d.Run("mini", func() (Result, error) {
		aged := time.Now().Add(-Stale - time.Minute)
		os.Chtimes(d.lockPath("mini"), aged, aged)
		time.Sleep(200 * time.Millisecond)
		if _, running := d.Running("mini"); !running {
			t.Error("a live build's lock aged out")
		}
		return Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Scratch also sweeps what a takeover that died left behind: moved-aside
// locks, and a takeover guard whose process is gone (no minute's wait).
func TestLeftoversOfADeadTakeoverAreSwept(t *testing.T) {
	d := Dir{t.TempDir()}
	dir := d.StateDir("mini")
	os.MkdirAll(dir, 0o755)
	aside := filepath.Join(dir, "lock.stale-abc")
	os.WriteFile(aside, []byte("1 x y\n"), 0o644)
	if _, err := d.Scratch("mini"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(aside); err == nil {
		t.Error("a moved-aside lock was left")
	}
	os.WriteFile(d.lockPath("mini"), []byte(fmt.Sprintf("%d 2000-01-01T00:00:00Z x\n", deadPid(t))), 0o644)
	os.WriteFile(d.lockPath("mini")+".takeover", []byte(fmt.Sprintf("%d\n", deadPid(t))), 0o644)
	unlock, err := d.Lock("mini")
	if err != nil {
		t.Fatalf("a dead takeover's guard blocked the lock: %v", err)
	}
	unlock()
}
