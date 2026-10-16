package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cli(t *testing.T, db string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(context.Background(), append(args, "--db", db), &out); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestCLIEndToEnd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	t.Chdir(t.TempDir()) // outside any git repository: the only workspace applies
	if out := cli(t, db, "import", fixture); !strings.Contains(out, "imported snapshot-20261015T120000Z") {
		t.Fatal(out)
	}
	for args, want := range map[string]string{
		"callers Shop.Orders.total/1 --depth 2": "⇄ cross-repo",
		"impact public.orders":                  "impact of TBL public.orders",
		"find billing":                          "MOD shop-app:Shop.Billing",
		"table orders --format json":            `"TBL public.orders"`,
		"status":                                "7 functions",
		"mermaid Shop.Orders.total/1 --max 2":   "+1 more → cartographer impact shop-app:Shop.Orders.total/1",
		"embed public.orders":                   "```mermaid\ngraph LR",
		"diff snapshot-20261015T120000Z snapshot-20261015T120000Z": "## cartographer impact",
	} {
		if out := cli(t, db, strings.Fields(args)...); !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", args, want, out)
		}
	}
	if out := cli(t, db, "validate-facts", fixture+"/facts/shop-app.json"); !strings.HasPrefix(out, "ok") {
		t.Error(out)
	}
}

func TestNoSnapshotMessage(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"status", "--db", filepath.Join(t.TempDir(), "x.db")}, &out)
	if err == nil || !strings.Contains(err.Error(), "cartographer pull") {
		t.Fatalf("got %v", err)
	}
}

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "carto-cli-")
	os.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(dir, "config.toml"))
	os.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	os.Setenv("CARTOGRAPHER_NO_REBUILD", "1") // tests opt in to rebuilds explicitly
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestBuildImportAndFreshness builds a workspace over a real git repo whose
// plugin replays the fixture facts, then queries from inside the repo after a
// local edit: the edited file is marked ⚠ and the header reports staleness.
func TestBuildImportAndFreshness(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "shop-app")
	os.MkdirAll(filepath.Join(repo, "lib/shop"), 0o755)
	os.WriteFile(filepath.Join(repo, "lib/shop/billing.ex"), []byte("defmodule Shop.Billing do end\n"), 0o644)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "init")

	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot/facts/shop-app.json")
	ws := filepath.Join(root, "map")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), []byte(`
name = "mini"
[[repos]]
name = "shop-app"
url = "https://github.com/example/shop-app"
path = "../shop-app"
plugins = ["replay"]
[plugins.replay]
command = ["cat", "`+fixture+`"]
`), 0o644)

	db := filepath.Join(t.TempDir(), "carto.db")
	out := cli(t, db, "build", "--workspace", ws, "--out", filepath.Join(root, "snap"), "--import")
	if !strings.Contains(out, "imported snapshot-") {
		t.Fatal(out)
	}
	os.WriteFile(filepath.Join(repo, "lib/shop/billing.ex"), []byte("defmodule Shop.Billing do\nend\n"), 0o644)
	t.Chdir(repo)
	out = cli(t, db, "callers", "Shop.Orders.total/1", "--no-pull")
	if !strings.Contains(out, "shop-app 1 files changed locally") || !strings.Contains(out, "lib/shop/billing.ex:5 ⚠") {
		t.Errorf("freshness not applied:\n%s", out)
	}
	out = cli(t, db, "status", "--no-pull")
	if !strings.Contains(out, "0 commits behind, 1 files changed ⚠") {
		t.Errorf("status:\n%s", out)
	}
	if out := cli(t, db, "mapped", "--no-pull"); out != "mapped\n" {
		t.Errorf("mapped = %q", out)
	}
	t.Chdir(root)
	var buf bytes.Buffer
	if err := run(context.Background(), []string{"mapped", "--db", db, "--no-pull"}, &buf); err != errNotMapped {
		t.Errorf("outside a repo: %v", err)
	}
}

func TestExportFlagsValidated(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	t.Chdir(t.TempDir())
	cli(t, db, "import", fixture)
	for _, args := range [][]string{
		{"export", "--around", "Shop.Orders.total/1", "-o", "x.pdf"},
		{"export", "--pdf", "--svg", "--around", "x", "-o", "x"},
		{"export", "--pdf", "--around", "Shop.Orders.total/1"},
		{"export", "--pdf", "-o", filepath.Join(t.TempDir(), "x.pdf")},
		{"serve", "--port", "-1"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), append(args, "--db", db), &out); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

func TestFlagsBeforeTheCommand(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	t.Chdir(t.TempDir())
	cli(t, db, "import", fixture)
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--db", db, "--no-pull", "find", "billing", "--limit", "1"}, &out); err != nil || !strings.Contains(out.String(), "MOD shop-app:Shop.Billing") {
		t.Errorf("leading flags: %v\n%s", err, out.String())
	}
	for _, args := range [][]string{{"--db", db}, {"--bogus", "find", "x"}} {
		err := run(context.Background(), args, &out)
		if err == nil || !strings.Contains(err.Error(), "put the command first") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestNegativeOffsetCLI(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot")
	t.Chdir(t.TempDir())
	cli(t, db, "import", fixture)
	if out := cli(t, db, "callers", "Shop.Orders.total/1", "--offset", "-3", "--no-pull"); !strings.Contains(out, "Shop.Billing.charge/2") {
		t.Error(out)
	}
}
