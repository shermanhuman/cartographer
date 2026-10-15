// Package server serves the browser UI and its JSON API on the loopback
// interface only. Every API response is computed from the store for one
// snapshot (?snapshot=ID, default newest).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/render"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/web"
)

// DefaultPort is the port `cartographer serve` binds by default.
const DefaultPort = 7878

// Options configures the server.
type Options struct {
	Store   *store.Store
	Version string
	// Status reports freshness of the selected snapshot against the
	// working copies (commits behind, changed files). The UI banner shows a
	// string as-is, or an object's "summary" field and marks it stale when
	// its "stale" field is true. nil (the default) means unknown.
	Status func(ctx context.Context, snapshot string) (any, error)
	// Now stamps generated_at; tests pin it.
	Now func() time.Time
	// Default picks the snapshot when the request names none (e.g. the map
	// of the repository serve was started in). nil means the newest. It may
	// be slow (it can run git); the server calls it again only when the set
	// of imported snapshots changes.
	Default func(ctx context.Context) (string, error)
}

// IsLoopback reports whether host (a name or IP, without port) is the
// loopback interface.
func IsLoopback(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listen binds host:port, refusing anything but a loopback address: the map
// holds private code metadata and has no authentication.
func Listen(host string, port int) (net.Listener, error) {
	if !IsLoopback(host) {
		return nil, fmt.Errorf("server: refusing to bind %q: cartographer serves on loopback (127.0.0.1) only", host)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// Serve runs h on ln until ctx is cancelled.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			srv.Shutdown(shut)
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// New builds the handler: static UI at /, JSON API under /api/.
func New(o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &api{o: o}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshots", a.snapshots)
	mux.HandleFunc("GET /api/catalog", a.with(func(r *http.Request, q *query.Q) (any, error) { return catalog(r.Context(), q) }))
	mux.HandleFunc("GET /api/overview", a.with(func(r *http.Request, q *query.Q) (any, error) { return overview(r.Context(), q) }))
	mux.HandleFunc("GET /api/files", a.with(func(r *http.Request, q *query.Q) (any, error) { return files(r.Context(), q) }))
	mux.HandleFunc("GET /api/schema", a.with(func(r *http.Request, q *query.Q) (any, error) { return schemaIndex(r.Context(), q) }))
	mux.HandleFunc("GET /api/annotations", a.with(func(r *http.Request, q *query.Q) (any, error) { return annotations(r.Context(), q) }))
	mux.HandleFunc("GET /api/status", a.with(a.status))
	mux.HandleFunc("GET /api/entity/{id...}", a.with(func(r *http.Request, q *query.Q) (any, error) {
		return entity(r.Context(), q, r.PathValue("id"))
	}))
	mux.HandleFunc("GET /api/graph", a.with(func(r *http.Request, q *query.Q) (any, error) {
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		return graph(r.Context(), q, r.URL.Query().Get("id"), depth)
	}))
	mux.HandleFunc("GET /api/embed", a.text(func(r *http.Request, q *query.Q) (string, error) {
		return render.Embed(r.Context(), q, r.URL.Query().Get("id"))
	}))
	mux.HandleFunc("GET /api/mermaid", a.text(func(r *http.Request, q *query.Q) (string, error) {
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		max, _ := strconv.Atoi(r.URL.Query().Get("max"))
		return render.Mermaid(r.Context(), q, r.URL.Query().Get("id"), depth, max)
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown API endpoint "+r.URL.Path)
	})
	static, _ := fs.Sub(web.FS, ".")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Only the root serves an index; any other directory is not a page,
		// so it gets a 404 instead of a generated listing.
		if p := strings.Trim(r.URL.Path, "/"); p != "" {
			if fi, err := fs.Stat(static, p); err == nil && fi.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return loopbackOnly(securityHeaders(mux))
}

// loopbackOnly rejects requests whose Host is not loopback, so a page on
// another origin cannot reach the map through DNS rebinding.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !IsLoopback(host) {
			http.Error(w, "cartographer serves loopback clients only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

type api struct {
	o Options

	defMu  sync.Mutex
	defKey string // the snapshot ids the cached default was computed from
	defID  string
	defSet bool
}

// defaultSnapshot is Options.Default, memoized: it runs again only when a
// snapshot is imported or pruned, so a click does not cost a git scan. ""
// means the newest snapshot.
func (a *api) defaultSnapshot(ctx context.Context) string {
	if a.o.Default == nil {
		return ""
	}
	var key string
	if err := a.o.Store.DB.QueryRowContext(ctx, `SELECT coalesce(group_concat(id, ','), '') FROM (SELECT id FROM snapshots ORDER BY id)`).Scan(&key); err != nil {
		log.Printf("cartographer serve: listing snapshots: %v", err)
	}
	a.defMu.Lock()
	defer a.defMu.Unlock()
	if a.defSet && a.defKey == key {
		return a.defID
	}
	id, err := a.o.Default(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cartographer serve: no default snapshot for this directory (%v); using the newest", err)
		}
		id = ""
	}
	if ctx.Err() == nil { // a cancelled request must not cache a partial answer
		a.defKey, a.defID, a.defSet = key, id, true
	}
	return id
}

// snapshotQ selects ?snapshot= or the default snapshot.
func (a *api) snapshotQ(r *http.Request) (*query.Q, int, error) {
	id := r.URL.Query().Get("snapshot")
	if id != "" {
		var n int
		if err := a.o.Store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM snapshots WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if n == 0 {
			return nil, http.StatusNotFound, fmt.Errorf("no snapshot %q", id)
		}
	}
	if id == "" {
		id = a.defaultSnapshot(r.Context())
	}
	q, err := query.New(r.Context(), a.o.Store, id)
	if errors.Is(err, store.ErrNoSnapshot) {
		return nil, http.StatusNotFound, errors.New("no snapshot imported yet: run `cartographer pull` or `cartographer import <dir>`")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return q, 0, nil
}

func (a *api) with(f func(*http.Request, *query.Q) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, code, err := a.snapshotQ(r)
		if err != nil {
			writeError(w, code, err.Error())
			return
		}
		v, err := f(r, q)
		if err != nil {
			writeError(w, errCode(err), err.Error())
			return
		}
		writeJSON(w, v)
	}
}

func (a *api) text(f func(*http.Request, *query.Q) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, code, err := a.snapshotQ(r)
		if err != nil {
			writeError(w, code, err.Error())
			return
		}
		s, err := f(r, q)
		if err != nil {
			writeError(w, errCode(err), err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(s))
	}
}

// errNotFound marks API errors that are the caller's to fix.
type errNotFound struct{ msg string }

func (e errNotFound) Error() string { return e.msg }

func errCode(err error) int {
	var amb *query.ErrAmbiguous
	var nf *query.ErrNotFound
	var enf errNotFound
	if errors.As(err, &amb) || errors.As(err, &nf) || errors.As(err, &enf) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	if code >= 500 {
		log.Printf("cartographer serve: %d: %s", code, msg)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (a *api) snapshots(w http.ResponseWriter, r *http.Request) {
	list, err := a.o.Store.Snapshots(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []obj{}
	for _, si := range list {
		q := &query.Q{S: a.o.Store, Snapshot: si.ID}
		repos, err := q.Repos(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, obj{"id": si.ID, "workspace": si.Workspace, "created_at": si.CreatedAt, "repos": nonNil(repos)})
	}
	writeJSON(w, out)
}

func (a *api) status(r *http.Request, q *query.Q) (any, error) {
	ctx := r.Context()
	db := q.S.DB
	var ws, created, imported string
	if err := db.QueryRowContext(ctx, `SELECT workspace, created_at, imported_at FROM snapshots WHERE id = ?`, q.Snapshot).Scan(&ws, &created, &imported); err != nil {
		return nil, fmt.Errorf("status: snapshot %s: %w", q.Snapshot, err)
	}
	repos, err := q.Repos(ctx)
	if err != nil {
		return nil, err
	}
	counts := obj{}
	for _, c := range []struct{ key, table string }{
		{"modules", "modules"}, {"functions", "functions"}, {"call_sites", "calls"}, {"tables", "db_tables"},
		{"docs", "docs"}, {"annotations", "annotations"}, {"contexts", "contexts"}, {"files", "files"},
	} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+c.table+` WHERE snapshot_id = ?`, q.Snapshot).Scan(&n); err != nil {
			return nil, fmt.Errorf("status: counting %s: %w", c.table, err)
		}
		counts[c.key] = n
	}
	var resolved int
	if err := db.QueryRowContext(ctx, `SELECT count(to_id) FROM calls WHERE snapshot_id = ?`, q.Snapshot).Scan(&resolved); err != nil {
		return nil, fmt.Errorf("status: counting resolved calls: %w", err)
	}
	counts["resolved_call_sites"] = resolved
	review := obj{}
	rows, err := db.QueryContext(ctx, `SELECT status, count(*) FROM annotations WHERE snapshot_id = ? GROUP BY 1`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		review[s] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The snapshot a link without #snapshot= opens: the UI drops the
	// parameter only when the picked snapshot is this one.
	def := a.defaultSnapshot(ctx)
	if def == "" {
		if def, err = q.S.Latest(ctx); err != nil {
			return nil, err
		}
	}
	var fresh any
	if a.o.Status != nil {
		if fresh, err = a.o.Status(ctx, q.Snapshot); err != nil {
			fresh = obj{"summary": "freshness check failed: " + err.Error(), "stale": true}
		}
	}
	return obj{
		"snapshot": q.Snapshot, "default_snapshot": def, "workspace": ws, "created_at": created, "imported_at": imported,
		"repos": nonNil(repos), "counts": counts, "annotations": review, "freshness": fresh,
		"provenance": q.Provenance(ctx), "version": a.o.Version,
		"generated_at": a.o.Now().UTC().Format(time.RFC3339),
	}, nil
}
