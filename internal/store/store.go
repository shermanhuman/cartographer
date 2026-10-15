// Package store keeps imported snapshots in a single SQLite file.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DefaultKeep is how many snapshots Prune keeps by default.
const DefaultKeep = 10

// ErrNoSnapshot is returned when the store holds no snapshot.
var ErrNoSnapshot = errors.New("store: no snapshot imported")

type Store struct{ DB *sql.DB }

// Open opens (creating if needed) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := "file::memory:?_pragma=foreign_keys(1)"
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // each connection would get its own empty database
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{DB: db}, nil
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

// Prune keeps the newest keep snapshots and deletes the rest.
func (s *Store) Prune(ctx context.Context, keep int) error {
	all, err := s.Snapshots(ctx)
	if err != nil {
		return err
	}
	for i := keep; i < len(all); i++ {
		if err := s.Delete(ctx, all[i].ID); err != nil {
			return err
		}
	}
	return nil
}
