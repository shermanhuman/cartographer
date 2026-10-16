package rebuild

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// A local workspace is found by name: a configured directory first, then
// a sibling of the current checkout; never one with another name.
func TestFindWorkspace(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	app := filepath.Join(parent, "shop-app")
	gitInit(t, app)
	sibling := makeWorkspace(t, filepath.Join(parent, "shop-map"), "mini")
	makeWorkspace(t, filepath.Join(parent, "other-map"), "other")
	os.MkdirAll(filepath.Join(parent, "no-config"), 0o755)
	cur := fresh.Find(ctx, app)
	if got := FindWorkspace("mini", cur, nil); !samePath(got, sibling) {
		t.Errorf("sibling: got %q, want %q", got, sibling)
	}
	if got := FindWorkspace("third", cur, nil); got != "" {
		t.Errorf("unknown name found %q", got)
	}
	configured := makeWorkspace(t, filepath.Join(t.TempDir(), "elsewhere"), "mini")
	if got := FindWorkspace("mini", cur, []string{filepath.Join(t.TempDir(), "missing"), configured}); got != configured {
		t.Errorf("configured: got %q, want %q", got, configured)
	}
	// Outside git there are no siblings to scan; configured entries still apply.
	if got := FindWorkspace("mini", fresh.Checkout{}, nil); got != "" {
		t.Errorf("outside git: %q", got)
	}
	if got := FindWorkspace("mini", fresh.Checkout{}, []string{configured}); got != configured {
		t.Errorf("outside git, configured: %q", got)
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
	// A dead process's lock: old mtime.
	os.WriteFile(d.lockPath("mini"), []byte("1 2000-01-01T00:00:00Z\n"), 0o644)
	old := time.Now().Add(-Stale - time.Minute)
	os.Chtimes(d.lockPath("mini"), old, old)
	if _, ok := d.Running("mini"); ok {
		t.Error("a stale lock counts as running")
	}
	if _, err := d.Lock("mini"); err != nil {
		t.Errorf("stale lock not taken over: %v", err)
	}
}

// Run holds the lock around build and stamps the outcome: a failure backs
// off for FailBackoff and keeps the error for Status; a success clears it.
func TestRunStampsAndBacksOff(t *testing.T) {
	d := Dir{t.TempDir()}
	if !d.Due("mini") {
		t.Fatal("a never-built workspace is due")
	}
	boom := errors.New("plugin elixir: exit status 1\nmore detail")
	if err := d.Run("mini", func() error {
		os.WriteFile(d.LogPath("mini"), []byte("building…\n"), 0o644) // the caller writes the log
		if _, err := d.Lock("mini"); !errors.Is(err, ErrRunning) {
			t.Errorf("not locked during build: %v", err)
		}
		return boom
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
	if err := d.Run("mini", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if st := d.Status("mini"); st != "" {
		t.Errorf("Status after success = %q", st)
	}
	if !d.Due("mini") { // success never backs off: the next evidence triggers again
		t.Error("not due after a success")
	}
	// A held lock: Run reports it without building or stamping.
	unlock, _ := d.Lock("mini")
	defer unlock()
	ran := false
	if err := d.Run("mini", func() error { ran = true; return nil }); !errors.Is(err, ErrRunning) || ran {
		t.Errorf("Run under a lock: %v, ran %v", err, ran)
	}
	if st := d.Status("mini"); !strings.HasPrefix(st, "rebuilding since ") {
		t.Errorf("Status while locked = %q", st)
	}
}

// Workspace names become file names safely.
func TestFileNames(t *testing.T) {
	d := Dir{t.TempDir()}
	p := d.LogPath("my org/map name")
	if filepath.Dir(p) != filepath.Join(d.Path, "rebuild") || strings.ContainsAny(filepath.Base(p), "/ ") {
		t.Errorf("LogPath = %q", p)
	}
}
