package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/export"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
)

// copyFixture writes the mini fixture as another workspace and snapshot id,
// with its repos renamed by prefix.
func copyFixture(t *testing.T, workspace, id, prefix string) string {
	t.Helper()
	src, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		txt := strings.ReplaceAll(string(b), "shop-", prefix)
		if rel == "manifest.json" {
			txt = strings.Replace(txt, `"workspace": "mini"`, `"workspace": "`+workspace+`"`, 1)
			txt = strings.Replace(txt, "snapshot-20261015T120000Z", id, 1)
		}
		rel = strings.ReplaceAll(rel, "shop-", prefix)
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		return os.WriteFile(filepath.Join(dst, rel), []byte(txt), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// export renders the snapshot chosen from cwd, not the newest one.
func TestExportUsesTheSnapshotChosenFromCwd(t *testing.T) {
	if _, err := export.FindChrome(); err != nil {
		t.Skipf("skipping browser export: %v", err)
	}
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "import", copyFixture(t, "mini", "snapshot-20261015T120000Z", "shop-"))
	cli(t, db, "import", copyFixture(t, "other", "snapshot-20261015T130000Z", "zz-"))
	app := filepath.Join(t.TempDir(), "shop-app")
	os.MkdirAll(app, 0o755)
	gitIn(t, app, "init", "-q", "-b", "main")
	gitIn(t, app, "remote", "add", "origin", "https://github.com/example/shop-app")
	t.Chdir(app)
	out := filepath.Join(t.TempDir(), "x.svg")
	cli(t, db, "export", "--svg", "--around", "Shop.Orders.total/1", "-o", out, "--no-pull")
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "map snapshot-20261015T120000Z") {
		t.Errorf("exported the wrong snapshot:\n%.400s", b)
	}
}

func TestUnknownCommandFailsBeforeOpeningTheStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	err := run(context.Background(), []string{"bogus", "--db", filepath.Join(dir, "carto.db")}, &out)
	if err == nil || !strings.Contains(err.Error(), `unknown command "bogus"`) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the store was opened for an unknown command")
	}
}

func TestHelpFlagSucceedsAndListsTheCommandsFlags(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"find", "--help"}, &out); err != nil {
		t.Fatalf("find --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{"cartographer find <words>", "-limit", "-format"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
	for _, unwanted := range []string{"-snippet", "-annotation", "-port"} {
		if strings.Contains(help, unwanted) {
			t.Errorf("help lists %q:\n%s", unwanted, help)
		}
	}
}

func TestFormatIsValidated(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture := copyFixture(t, "mini", "snapshot-20261015T120000Z", "shop-")
	t.Chdir(t.TempDir())
	cli(t, db, "import", fixture)
	for _, args := range [][]string{
		{"find", "billing", "--format", "yaml"},
		{"mermaid", "Shop.Orders.total/1", "--format", "json"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), append(args, "--db", db, "--no-pull"), &out); err == nil || !strings.Contains(err.Error(), "--format") {
			t.Errorf("%v: %v\n%s", args, err, out.String())
		}
	}
	if out := cli(t, db, "impact", "Shop.Orders.total/1", "--limit", "1", "--format", "json", "--no-pull"); !strings.Contains(out, `"+3 more: offset=1"`) {
		t.Errorf("impact --limit --format json:\n%s", out)
	}
}

// serve's status endpoint starts a due background pull, so a long-running
// serve sees snapshots published after it started.
func TestServeStatusStartsADuePull(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("workspaces = [\"example/map\"]\n"), 0o644)
	t.Setenv("CARTOGRAPHER_CONFIG", cfg)
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Import(context.Background(), copyFixture(t, "mini", "snapshot-20261015T120000Z", "shop-")); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	sess := &session.Session{Store: s, DataDir: data, Cwd: t.TempDir(), AutoPull: true, Background: true, Log: io.Discard,
		GitHub: &snapshot.GitHub{API: srv.URL, HTTP: srv.Client()}}
	if _, err := serveStatus(sess)(context.Background(), "snapshot-20261015T120000Z"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hits:
	case <-time.After(5 * time.Second):
		t.Fatal("status did not start a pull")
	}
	// Let the pull record its failure before the temp dirs go.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m, _ := filepath.Glob(filepath.Join(data, "stamps", "*")); len(m) > 0 {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
}

func TestSchemaPluginsDigestCoversOnlyReferencedPlugins(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
	}
	write("extract-postgres-schema/capture.exs", "v1")
	write("extract-elixir/extract.exs", "v1")
	cmd := []string{"elixir", "{plugins}/extract-postgres-schema/capture.exs", "--database", "x"}
	d1, err := schemaPluginsDigest(cmd, dir)
	if err != nil || d1 == "" {
		t.Fatalf("digest %q, %v", d1, err)
	}
	write("extract-elixir/extract.exs", "v2")
	if d2, _ := schemaPluginsDigest(cmd, dir); d2 != d1 {
		t.Error("an unrelated plugin edit changed the schema digest")
	}
	write("extract-postgres-schema/capture.exs", "v2")
	if d3, _ := schemaPluginsDigest(cmd, dir); d3 == d1 {
		t.Error("a schema plugin edit kept the digest")
	}
	if d, _ := schemaPluginsDigest([]string{"./dump-schema"}, dir); d == "" {
		t.Error("a command without bundled plugins needs a non-empty digest")
	}
}
