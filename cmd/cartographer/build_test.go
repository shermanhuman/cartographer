package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// build --fetch-only clones the repos and runs no plugin; build then works
// from the clone without fetching.
func TestBuildFetchOnly(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "upstream")
	os.MkdirAll(filepath.Join(repo, "lib"), 0o755)
	os.WriteFile(filepath.Join(repo, "lib/a.ex"), []byte("defmodule A do end\n"), 0o644)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "init")

	fixture, _ := filepath.Abs("../../testdata/workspace-mini/snapshot/facts/shop-app.json")
	ws := filepath.Join(root, "map")
	marker := filepath.Join(root, "plugin-ran")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), []byte(`
name = "mini"
[[repos]]
name = "shop-app"
url = "`+repo+`"
path = "../clone/shop-app"
plugins = ["replay"]
[plugins.replay]
command = ["sh", "-c", "touch `+marker+` && cat `+fixture+`"]
`), 0o644)

	db := filepath.Join(t.TempDir(), "carto.db")
	snap := filepath.Join(root, "snap")
	out := cli(t, db, "build", "--workspace", ws, "--fetch-only", "--out", snap)
	if !strings.Contains(out, "fetched") {
		t.Errorf("output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "clone/shop-app/.git")); err != nil {
		t.Fatalf("no clone: %v", err)
	}
	for _, p := range []string{marker, snap} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("--fetch-only created %s", p)
		}
	}
	if out := cli(t, db, "build", "--workspace", ws, "--out", snap); !strings.Contains(out, "built snapshot-") {
		t.Fatal(out)
	}
}
