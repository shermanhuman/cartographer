// Package store keeps imported snapshots in a single SQLite file.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/shermanhuman/cartographer/internal/bundle"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion is kept in PRAGMA user_version. The store is a cache of
// published snapshots, so a database at an older version is dropped and
// recreated rather than migrated; the next pull or import refills it. A
// newer one is refused (ErrNewerStore).
//
//	1 (stored as 0): initial schema
//	2: db_tables.kind
//	4: http_refs.origin, LiveView events (event_sends, event_handlers,
//	   hook_mounts, event_links), the edges view's 'event' kind
var schemaVersion = 4 // a var only so tests can pose as a later schema (SetSchemaVersionForTest)

// SchemaVersion is the schema this binary reads and writes.
func SchemaVersion() int { return schemaVersion }

// SetSchemaVersionForTest makes this binary act as schema v for a test and
// returns the restore function. Production code never calls it.
func SetSchemaVersionForTest(v int) (restore func()) {
	old := schemaVersion
	schemaVersion = v
	return func() { schemaVersion = old }
}

// DefaultFile is the default store's file name for schema v. Each schema
// after 4 gets its own file, so an older cartographer never opens a newer
// store: it only knows its own name. Schema 4 keeps the name it always had.
func DefaultFile(v int) string {
	if v <= 4 {
		return "carto.db"
	}
	return fmt.Sprintf("carto-v%d.db", v)
}

// IsDefaultFile reports whether name (a base name) is a default store's
// file name for some schema.
func IsDefaultFile(name string) bool {
	if name == "carto.db" {
		return true
	}
	var v int
	n, err := fmt.Sscanf(name, "carto-v%d.db", &v)
	return err == nil && n == 1 && v > 4 && name == DefaultFile(v)
}

// DataDir is $CARTOGRAPHER_DATA, else $XDG_DATA_HOME/cartographer or
// ~/.local/share/cartographer: where the default store lives.
func DataDir() string {
	if d := os.Getenv("CARTOGRAPHER_DATA"); d != "" {
		return d
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "cartographer")
}

// Resolve maps the default store recorded under any schema (a default file
// name in the data directory) to this schema's file there. Any other path,
// a carto.db elsewhere included, is the user's choice and is returned as
// is. A registration recorded before a schema change then follows the
// store.
func Resolve(path string) string {
	if path == "" || !IsDefaultFile(filepath.Base(path)) || !sameDir(filepath.Dir(path), DataDir()) {
		return path
	}
	return filepath.Join(filepath.Dir(path), DefaultFile(schemaVersion))
}

// IsDefaultStore reports whether path is a default store file, for any
// schema, in the data directory.
func IsDefaultStore(path string) bool {
	return IsDefaultFile(filepath.Base(path)) && sameDir(filepath.Dir(path), DataDir())
}

func sameDir(a, b string) bool {
	clean := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}

// DefaultKeep is how many snapshots per workspace Prune keeps by default.
const DefaultKeep = 10

// NoMapAdvice is the one answer to "there's no map": maps come from
// registered projects (0.3 and later), and registering one is the user's
// call. session.NoMapAdvice names the project when the checkout declares one.
const NoMapAdvice = "register a project with `cartographer project add owner/name` (registering is the user's call; `cartographer project list` shows what's registered)"

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

// ErrNewerStore means the database was written by a newer cartographer.
// Opening it here would drop every map in it (the rebuild below), and the
// newer release that wrote them would then rebuild the store again, so an
// old binary started by a stale PATH would wipe a store it can't read.
type ErrNewerStore struct{ Have, Want int }

func (e *ErrNewerStore) Error() string {
	return fmt.Sprintf("the store was written by a newer cartographer (schema %d; this binary, %s version %s, reads %d), so it's left untouched: an older cartographer is running, often one first on a stale PATH (check `which -a cartographer`); run the newer one, or upgrade (mise use -g github:shermanhuman/cartographer@latest), or pass --db for a separate store", e.Have, exe(), BinaryVersion, e.Want)
}

// migrate creates the schema, first dropping every table when the database
// was written at an older schemaVersion (the store is a cache of imported
// snapshots). A newer one is refused, never dropped (ErrNewerStore).
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
	if v > schemaVersion {
		return &ErrNewerStore{Have: v, Want: schemaVersion}
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
	if v > schemaVersion {
		return &ErrNewerStore{Have: v, Want: schemaVersion} // a newer one wrote it first
	}
	// Virtual tables go first (their shadow tables go with them), snapshots
	// last (everything else references it).
	for _, view := range []string{"edges", "nodes"} {
		if _, err := conn.ExecContext(ctx, `DROP VIEW IF EXISTS `+view); err != nil {
			return err
		}
	}
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
	// Source is main, head or published (see bundle.Manifest.Source).
	Source string
	// MapID is the content identity (bundle.MapID), "" when unknown.
	MapID string
	// InputsDigest is the manifest's inputs digest ("" for most published
	// and explicit builds).
	InputsDigest string
}

// Snapshots lists imported snapshots, newest first.
func (s *Store) Snapshots(ctx context.Context) ([]SnapshotInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, workspace, created_at, source, map_id, coalesce(json_extract(manifest, '$.inputs_digest'), '')
	  FROM snapshots ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotInfo
	for rows.Next() {
		var si SnapshotInfo
		if err := rows.Scan(&si.ID, &si.Workspace, &si.CreatedAt, &si.Source, &si.MapID, &si.InputsDigest); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// Newest returns the newest snapshot of workspace with the given source
// (ok false when there is none).
func (s *Store) Newest(ctx context.Context, workspace, source string) (SnapshotInfo, bool, error) {
	// One row, in Snapshots' order: project pages ask on every API call.
	var si SnapshotInfo
	err := s.DB.QueryRowContext(ctx, `SELECT id, workspace, created_at, source, map_id, coalesce(json_extract(manifest, '$.inputs_digest'), '')
	  FROM snapshots WHERE workspace = ? AND source = ? ORDER BY created_at DESC, id DESC LIMIT 1`, workspace, source).
		Scan(&si.ID, &si.Workspace, &si.CreatedAt, &si.Source, &si.MapID, &si.InputsDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotInfo{}, false, nil
	}
	if err != nil {
		return SnapshotInfo{}, false, err
	}
	return si, true, nil
}

// Info returns one snapshot's summary (ok false when it isn't imported).
func (s *Store) Info(ctx context.Context, id string) (SnapshotInfo, bool, error) {
	all, err := s.Snapshots(ctx)
	if err != nil {
		return SnapshotInfo{}, false, err
	}
	for _, si := range all {
		if si.ID == id {
			return si, true, nil
		}
	}
	return SnapshotInfo{}, false, nil
}

// Manifest returns a snapshot's manifest as imported.
func (s *Store) Manifest(ctx context.Context, id string) (*bundle.Manifest, error) {
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT manifest FROM snapshots WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var m bundle.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Workspace names the workspace of a snapshot ("" when unknown).
func (s *Store) Workspace(ctx context.Context, id string) (string, error) {
	var ws string
	err := s.DB.QueryRowContext(ctx, `SELECT workspace FROM snapshots WHERE id = ?`, id).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ws, err
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

// DeleteSource removes workspace's snapshots of source (and everything
// imported with them) and returns how many.
func (s *Store) DeleteSource(ctx context.Context, workspace, source string) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM snapshots WHERE workspace = ? AND source = ?`, workspace, source)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := deleteSnapshot(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
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

// RepoHasCode reports whether repo has at least one module or function in
// snapshot.
func (s *Store) RepoHasCode(ctx context.Context, snapshot, repo string) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM modules WHERE snapshot_id = ? AND repo = ?)
		OR EXISTS(SELECT 1 FROM functions WHERE snapshot_id = ? AND repo = ?)`, snapshot, repo, snapshot, repo).Scan(&ok)
	return ok, err
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

// exe names this binary for ErrNewerStore ("cartographer" when unknown).
func exe() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "cartographer"
}
