// Package store keeps imported snapshots in a single SQLite file.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion is kept in PRAGMA user_version. The store is a cache of
// published snapshots, so a database at any other version is dropped and
// recreated rather than migrated; the next pull or import refills it.
//
//	1 (stored as 0): initial schema
//	2: db_tables.kind
const schemaVersion = 2

// DefaultKeep is how many snapshots per workspace Prune keeps by default.
const DefaultKeep = 10

// ErrNoSnapshot is returned when the store holds no snapshot.
var ErrNoSnapshot = errors.New("store: no snapshot imported")

type Store struct{ DB *sql.DB }

// Open opens (creating if needed) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := "file::memory:?_pragma=foreign_keys(1)"
	if path != ":memory:" {
		var err error
		if dsn, err = fileURI(path); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // each connection would get its own empty database
	}
	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{DB: db}, nil
}

// fileURI builds the SQLite URI for path. The path is escaped, so a '#',
// '?' or '%' in it names the file rather than starting URI syntax.
func fileURI(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows: file:///C:/...
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}
	return u.String(), nil
}

// migrate creates the schema, first dropping every table when the database
// was written at another schemaVersion.
func migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var v int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v == schemaVersion {
		return nil
	}
	// IMMEDIATE takes the write lock up front, so two processes opening an
	// old database don't both rebuild it.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err := rebuild(ctx, conn); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func rebuild(ctx context.Context, conn *sql.Conn) error {
	var v int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v == schemaVersion {
		return nil // another process rebuilt it first
	}
	// Virtual tables go first (their shadow tables go with them), snapshots
	// last (everything else references it).
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	  ORDER BY sql LIKE 'CREATE VIRTUAL TABLE%' DESC, name = 'snapshots', name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS "`+strings.ReplaceAll(t, `"`, `""`)+`"`); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

func (s *Store) Close() error { return s.DB.Close() }

// SnapshotInfo summarises one imported snapshot.
type SnapshotInfo struct {
	ID        string
	Workspace string
	CreatedAt string
}

// Snapshots lists imported snapshots, newest first.
func (s *Store) Snapshots(ctx context.Context) ([]SnapshotInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, workspace, created_at FROM snapshots ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotInfo
	for rows.Next() {
		var si SnapshotInfo
		if err := rows.Scan(&si.ID, &si.Workspace, &si.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// Latest returns the newest snapshot id.
func (s *Store) Latest(ctx context.Context) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM snapshots ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSnapshot
	}
	return id, err
}

// Delete removes one snapshot and everything imported with it.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteSnapshot(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteSnapshot(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM entities WHERE snapshot_id = ?`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
	return err
}

// Prune keeps the newest keep snapshots of each workspace and deletes the
// rest, so a busy workspace never evicts a quiet one's snapshots.
func (s *Store) Prune(ctx context.Context, keep int) error {
	all, err := s.Snapshots(ctx)
	if err != nil {
		return err
	}
	kept := map[string]int{}
	for _, si := range all { // newest first
		if kept[si.Workspace] < keep {
			kept[si.Workspace]++
			continue
		}
		if err := s.Delete(ctx, si.ID); err != nil {
			return err
		}
	}
	return nil
}

// RepoInfo is a repository recorded in a snapshot.
type RepoInfo struct{ Name, URL, Revision string }

// Repos lists the repositories in a snapshot.
func (s *Store) Repos(ctx context.Context, snapshot string) ([]RepoInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, url, revision FROM repos WHERE snapshot_id = ? ORDER BY name`, snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoInfo
	for rows.Next() {
		var r RepoInfo
		if err := rows.Scan(&r.Name, &r.URL, &r.Revision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
