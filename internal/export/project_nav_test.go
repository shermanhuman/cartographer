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

// A #project= link opens that project's newest main build: every API call
// carries the project, navigating inside the map keeps it in the address,
// and switching it reloads onto the other project's data.
func TestProjectLinkFollowsProject(t *testing.T) {
	needChrome(t)
	chrome, _ := FindChrome()
	const alpha, beta = "snapshot-20261121T000000Z", "snapshot-20261123T000000Z"
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	for _, v := range [][2]string{{"alpha", alpha}, {"beta", beta}} {
		if _, err := s.Import(ctx, v2Project(t, v[0], v[1])); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var seen []string // "path project" of each API call
	h := server.New(server.Options{Store: s, Version: "test"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mu.Lock()
			seen = append(seen, r.URL.Path+" "+r.URL.Query().Get("project")+" "+r.URL.Query().Get("snapshot"))
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
	allCarry := func(project string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			t.Fatal("no API calls")
		}
		snapshots := false
		for _, got := range seen {
			path, rest, _ := strings.Cut(got, " ")
			p, _, _ := strings.Cut(rest, " ")
			snapshots = snapshots || path == "/api/snapshots"
			if p != project {
				t.Errorf("API call %s carried project %q, want %s (all: %q)", path, p, project, seen)
				return
			}
		}
		if !snapshots {
			t.Errorf("the snapshot list wasn't fetched: %q", seen)
		}
		seen = nil
	}

	// The newest snapshot overall is beta's; the link opens alpha's.
	if err := chromedp.Do(tctx, chromedp.Navigate(srv.URL+"/#view=home&project=alpha")); err != nil {
		t.Fatal(err)
	}
	sidebar(alpha)
	allCarry("alpha")

	// Navigating inside the map keeps the project in the address.
	hash, err := chromedp.Run(tctx, chromedp.Evaluate[string](`document.querySelector('[data-view="modules"]').click(); location.hash`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "project=alpha") || !strings.Contains(hash, "view=modules") {
		t.Errorf("after navigating, the address is %q; want view=modules with project=alpha", hash)
	}

	// The logo goes home inside the map: the project stays (R1-M1).
	if _, err := chromedp.Run(tctx, chromedp.Evaluate[any](`document.querySelector("a.brand").click()`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	sidebar(alpha)
	if hash := evalStr(t, tctx, `location.hash`); !strings.Contains(hash, "project=alpha") || !strings.Contains(hash, "view=home") {
		t.Errorf("after the logo, the address is %q; want view=home with project=alpha", hash)
	}
	// Copy link keeps the project and pins no snapshot (R1-m2).
	copied := evalStr(t, tctx, `new Promise(res => { navigator.clipboard.writeText = t => { res(t); return Promise.resolve(); }; document.getElementById("copy-link").click(); })`)
	if !strings.Contains(copied, "project=alpha") || strings.Contains(copied, "snapshot=") {
		t.Errorf("copied link %q; want project=alpha and no snapshot", copied)
	}

	// A rebuild imported after the page loaded doesn't change it: later
	// calls carry the build the page loaded (R2-n6).
	if _, err := s.Import(ctx, v2Project(t, "alpha", "snapshot-20261122T000000Z")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := chromedp.Run(tctx, chromedp.Evaluate[any](`document.querySelector('[data-view="tables"]').click()`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	lazy := slices.Clone(seen)
	mu.Unlock()
	if len(lazy) == 0 {
		t.Fatal("opening the tables view made no API call")
	}
	for _, got := range lazy {
		if !strings.HasSuffix(got, " alpha "+alpha) {
			t.Errorf("after a rebuild, %q isn't pinned to the loaded build %s", got, alpha)
		}
	}

	// Switching the project reloads onto its data.
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := chromedp.Run(tctx, chromedp.Evaluate[string](`location.hash="view=home&project=beta"`)); err != nil {
		t.Fatal(err)
	}
	sidebar(beta)
	allCarry("beta")
}

func evalStr(t *testing.T, ctx context.Context, expr string) string {
	t.Helper()
	v, err := chromedp.Run(ctx, chromedp.Evaluate[string](expr, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v
}

// v2Project copies the format-2 fixture (source main) as workspace ws with
// snapshot id.
func v2Project(t *testing.T, ws, id string) string {
	t.Helper()
	const src = "../../testdata/workspace-v2/snapshot"
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
			raw = []byte(strings.NewReplacer(`"workspace": "v2"`, `"workspace": "`+ws+`"`, "snapshot-20261120T000000Z", id).Replace(string(raw)))
		}
		return os.WriteFile(filepath.Join(dir, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
