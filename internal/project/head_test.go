package project_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// headRunner is a runner whose --head builds go into a scratch store.
func (x *h) headRunner() (*project.Runner, *store.Store) {
	x.t.Helper()
	db := filepath.Join(x.t.TempDir(), "head.db")
	hs, err := store.Open(db)
	if err != nil {
		x.t.Fatal(err)
	}
	x.t.Cleanup(func() { hs.Close() })
	hr := x.runner()
	hr.Store, hr.DB = hs, db
	return hr, hs
}

func schemaDoc(table string) string {
	return `{"facts_version":2,"producer":{"name":"schema","version":"1"},"tables":[{"id":"public.` + table + `","name":"` + table + `"}]}`
}

// A --head build's schema step without cache_key is keyed on the working
// copy, not the commit (A-M2): two head builds separated by an uncommitted
// schema change map the changed tables, not the cached ones.
func TestHeadSchemaFollowsUncommittedChange(t *testing.T) {
	x := newH(t)
	admin, sales, _ := x.world()
	x.push("admin", map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`,
			"[schema]", `capture = ["sh", "-c", "cat {root}/schema.json"]`),
		"schema.json": schemaDoc("orders"),
	})
	git(t, admin, "pull", "-q", "https://github.com/acme/admin", "main")
	hr, hs := x.headRunner()
	tables := func(snap string) []string {
		t.Helper()
		rows, err := hs.DB.Query(`SELECT name FROM db_tables WHERE snapshot_id = ? ORDER BY name`, snap)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}
	res, err := hr.Head(context.Background(), sales)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables(res.Snapshot); len(got) != 1 || got[0] != "orders" {
		t.Fatalf("first head build tables = %v", got)
	}
	writeFiles(t, admin, map[string]string{"schema.json": schemaDoc("invoices")}) // not committed
	x.advance(time.Minute)
	res, err = hr.Head(context.Background(), sales)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables(res.Snapshot); len(got) != 1 || got[0] != "invoices" {
		t.Errorf("after an uncommitted schema change, head build tables = %v, want [invoices]", got)
	}
}

// A --head build stages the working copy with its executable bits (A-M3):
// a checked-in script the schema step runs works as it does from the
// fetched archive.
func TestHeadKeepsExecutableScripts(t *testing.T) {
	x := newH(t)
	admin, sales, _ := x.world()
	writeFiles(t, admin, map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`,
			"[schema]", `prepare = "./scripts/prepare-schema"`, `capture = ["sh", "-c", "cat {root}/schema.json"]`),
		"scripts/prepare-schema": "#!/bin/sh\necho '" + schemaDoc("orders") + "' > schema.json\n",
	})
	if err := os.Chmod(filepath.Join(admin, "scripts", "prepare-schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, admin, "add", "-A")
	git(t, admin, "commit", "-q", "-m", "schema script")
	hr, hs := x.headRunner()
	res, err := hr.Head(context.Background(), sales)
	if err != nil {
		t.Fatalf("head build with an executable prepare script: %v", err)
	}
	var n int
	hs.DB.QueryRow(`SELECT count(*) FROM db_tables WHERE snapshot_id = ? AND name = 'orders'`, res.Snapshot).Scan(&n)
	if n != 1 {
		t.Errorf("orders tables = %d, want 1", n)
	}
}
