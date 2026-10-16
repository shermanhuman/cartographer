package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
)

func emptyBundle(id, ws, created string) *bundle.Bundle {
	return &bundle.Bundle{Manifest: bundle.Manifest{SnapshotFormat: bundle.Format, ID: id, Workspace: ws, CreatedAt: created, Contexts: bundle.ContextCfg{Depth: 2}}}
}

// A quiet workspace keeps its snapshot however many newer ones another
// workspace imports.
func TestPrunePerWorkspace(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.ImportBundle(ctx, emptyBundle("snapshot-B1", "B", "2026-10-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DefaultKeep+2; i++ {
		if err := s.ImportBundle(ctx, emptyBundle(fmt.Sprintf("snapshot-A%02d", i), "A", fmt.Sprintf("2026-10-02T%02d:00:00Z", i))); err != nil {
			t.Fatal(err)
		}
		if err := s.Prune(ctx, DefaultKeep); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, x := range all {
		per[x.Workspace]++
	}
	if per["A"] != DefaultKeep || per["B"] != 1 {
		t.Errorf("snapshots per workspace = %v, want A:%d B:1", per, DefaultKeep)
	}
	if all[len(all)-1].ID != "snapshot-B1" {
		t.Errorf("oldest kept = %s", all[len(all)-1].ID)
	}
}

// '#', '?' and '%' in the path must not change which file is opened.
func TestOpenEscapesPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my#maps?x=1%20")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "carto.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportBundle(context.Background(), emptyBundle("snapshot-x", "W", "2026-10-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(p); err != nil {
		entries, _ := os.ReadDir(filepath.Dir(dir))
		t.Fatalf("database not at %s (%v); siblings: %v", p, err, entries)
	}
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if id, err := s.Latest(context.Background()); err != nil || id != "snapshot-x" {
		t.Errorf("reopen: Latest = %q, %v", id, err)
	}
}

// A database written by an older schema is dropped and recreated: the store
// is a cache that the next pull or import refills.
func TestOpenRecreatesOldSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "carto.db")
	old, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE snapshots (id TEXT PRIMARY KEY, workspace TEXT NOT NULL, created_at TEXT NOT NULL, imported_at TEXT NOT NULL, manifest TEXT NOT NULL)`,
		`CREATE TABLE db_tables (snapshot_id TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE VIRTUAL TABLE entities USING fts5(snapshot_id UNINDEXED, id UNINDEXED, kind UNINDEXED, label, words)`,
		`INSERT INTO snapshots VALUES ('snapshot-old', 'W', 't', 't', '{}')`,
		`INSERT INTO db_tables VALUES ('snapshot-old', 'public.orders', 'orders')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	old.Close()

	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var v, kind, snaps int
	s.DB.QueryRow(`PRAGMA user_version`).Scan(&v)
	s.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('db_tables') WHERE name = 'kind'`).Scan(&kind)
	s.DB.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&snaps)
	if v != schemaVersion || kind != 1 || snaps != 0 {
		t.Errorf("after open: user_version %d (want %d), kind column %d, snapshots %d", v, schemaVersion, kind, snaps)
	}
	if err := s.ImportBundle(context.Background(), emptyBundle("snapshot-x", "W", "2026-10-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The current version is left alone.
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if id, err := s.Latest(context.Background()); err != nil || id != "snapshot-x" {
		t.Errorf("reopen at current version: Latest = %q, %v", id, err)
	}
}
