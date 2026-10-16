package export

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/shermanhuman/cartographer/internal/server"
	"github.com/shermanhuman/cartographer/internal/store"
)

// Following a link to another snapshot in the same document reloads the
// map, and every API call after it carries the new snapshot.
func TestSnapshotLinkReloads(t *testing.T) {
	needChrome(t)
	chrome, _ := FindChrome()
	const newer = "snapshot-20261016T120000Z"
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := s.Import(ctx, "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, renamedFixture(t, newer)); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var seen []string // snapshot param of each API call
	h := server.New(server.Options{Store: s, Version: "test"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/snapshots" {
			mu.Lock()
			seen = append(seen, r.URL.Query().Get("snapshot"))
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]), chromedp.ExecPath(chrome))
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	cctx, cancel := chromedp.NewContext(actx, chromedp.WithErrorf(func(string, ...any) {}))
	defer cancel()
	tctx, cancelT := context.WithTimeout(cctx, 60*time.Second)
	defer cancelT()

	// sidebar waits for a loaded page showing want; it re-evaluates
	// because a reload discards the JavaScript context mid-wait.
	sidebar := func(want string) {
		t.Helper()
		var now string
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			got, err := chromedp.Run(tctx, chromedp.Evaluate[string](`document.body.dataset.ready === "1" ? document.getElementById("sidebar-snapshot").textContent : ""`))
			if err == nil && strings.HasPrefix(got, want+" ") {
				return
			}
			now = got
		}
		t.Fatalf("sidebar shows %q, want %s", now, want)
	}
	if err := chromedp.Do(tctx, chromedp.Navigate(srv.URL+"/#view=home&snapshot="+snap)); err != nil {
		t.Fatal(err)
	}
	sidebar(snap)
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := chromedp.Run(tctx, chromedp.Evaluate[string](`location.hash="view=home&snapshot=`+newer+`"`)); err != nil {
		t.Fatal(err)
	}
	sidebar(newer)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no API calls after following the link")
	}
	for _, got := range seen {
		if got != newer {
			t.Errorf("API call after the link used snapshot %q, want %s (all: %q)", got, newer, seen)
			break
		}
	}
}

// renamedFixture copies the fixture snapshot under another snapshot id.
func renamedFixture(t *testing.T, id string) string {
	t.Helper()
	const src = "../../testdata/workspace-mini/snapshot"
	dir := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "manifest.json" {
			raw = []byte(strings.Replace(string(raw), snap, id, 1))
			raw = []byte(strings.Replace(string(raw), "2026-10-15T12:00:00Z", "2026-10-16T12:00:00Z", 1))
		}
		return os.WriteFile(filepath.Join(dir, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
