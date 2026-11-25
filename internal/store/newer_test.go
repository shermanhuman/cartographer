package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// An older cartographer opening a store a newer one wrote must leave it
// alone: dropping it (as it does an older store) wiped every map when a
// stale PATH started an old binary.
func TestNewerStoreIsRefusedNotDropped(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "carto.db")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err = Open(db)
	var newer *ErrNewerStore
	if !errors.As(err, &newer) || newer.Have != schemaVersion+1 {
		t.Fatalf("Open of a newer store: %v, want ErrNewerStore", err)
	}
	// Still there: put the version back and read it.
	s2 := reopenAt(t, db, schemaVersion)
	defer s2.Close()
	var n int
	if err := s2.DB.QueryRowContext(ctx, `SELECT count(*) FROM snapshots`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshots after the refused open: %d, %v; want 1", n, err)
	}
}

// reopenAt sets user_version on db without migrating, then opens it.
func reopenAt(t *testing.T, db string, v int) *Store {
	t.Helper()
	dsn, err := fileURI(db)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The locked re-check alone: a newer release that wins the write lock first
// must not have its store dropped by the older one waiting on it (R1-m1).
func TestRebuildRefusesANewerStore(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "carto.db")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Import(ctx, "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	err = rebuild(ctx, conn)
	conn.ExecContext(ctx, `ROLLBACK`)
	var newer *ErrNewerStore
	if !errors.As(err, &newer) {
		t.Fatalf("rebuild of a newer store: %v, want ErrNewerStore", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM snapshots`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshots after the refused rebuild: %d, %v", n, err)
	}
}
