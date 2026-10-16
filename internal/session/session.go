// Package session prepares a query for the current directory: pull newer
// snapshots when due, pick the snapshot whose workspace covers the current
// repository, and mark results against local changes.
package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
)

// CacheTTL is how long a session reuses its snapshot choice and freshness
// report for the same directory. MCP tool calls arrive in bursts; this keeps
// each burst to one round of git execs.
var CacheTTL = 10 * time.Second

// PullTimeout bounds a foreground automatic pull (one-shot CLI queries), so
// an offline or slow network delays a query by at most this much.
// `cartographer pull` is not bounded.
var PullTimeout = 30 * time.Second

// BackgroundPullTimeout bounds a background pull (MCP server, serve): the
// query answers from the snapshot already pulled and the next one sees the
// new snapshot.
var BackgroundPullTimeout = 10 * time.Minute

type noCacheKey struct{}

// WithoutCache marks ctx so the query recomputes freshness instead of
// reusing the CacheTTL memo (e.g. an explicit status check after an edit).
func WithoutCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey{}, true)
}

// Session is safe for concurrent use (the MCP server runs tool calls in
// parallel) once its exported fields are set.
type Session struct {
	Store    *store.Store
	DataDir  string
	Cwd      string
	Snapshot string // explicit snapshot id; "" selects by cwd
	AutoPull bool
	// Background pulls off the query path (long-lived processes); otherwise
	// the pull runs in the foreground within PullTimeout.
	Background bool
	Log        io.Writer // warnings (pull failures); nil discards
	GitHub     *snapshot.GitHub

	ghMu   sync.Mutex // guards GitHub's lazy creation
	pullMu sync.Mutex // one auto-pull at a time; guards warned
	warned bool

	mu   sync.Mutex // guards memo; held while computing it so bursts share one computation
	memo *memo
}

// memo caches what one directory resolves to.
type memo struct {
	at      time.Time
	cwd     string
	co      fresh.Checkout
	chosen  string                   // snapshot chosen by cwd ("" not yet)
	reports map[string]*fresh.Report // snapshot id -> freshness
}

// DataDir is $CARTOGRAPHER_DATA, else $XDG_DATA_HOME/cartographer or
// ~/.local/share/cartographer.
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

func (s *Session) logf(format string, a ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", a...)
	}
}

// Puller returns a puller for this session's store.
func (s *Session) Puller() *snapshot.Puller {
	s.ghMu.Lock()
	if s.GitHub == nil {
		s.GitHub = snapshot.NewGitHub()
	}
	gh := s.GitHub
	s.ghMu.Unlock()
	return &snapshot.Puller{Store: s.Store, GitHub: gh, DataDir: s.DataDir}
}

// PullDue pulls every configured workspace whose last check is older than
// snapshot.AutoInterval. Failures are reported once and never fatal. A call
// made while another pull is running returns at once. With Background set it
// returns immediately and pulls in a goroutine.
func (s *Session) PullDue(ctx context.Context) {
	if !s.pullMu.TryLock() {
		return
	}
	if s.Background {
		go func() {
			defer s.pullMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), BackgroundPullTimeout)
			defer cancel()
			s.pullDue(ctx)
		}()
		return
	}
	defer s.pullMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	s.pullDue(ctx)
}

// pullDue does the pulls. The caller holds pullMu.
func (s *Session) pullDue(ctx context.Context) {
	cfg, err := userconfig.Load()
	if err != nil || len(cfg.Workspaces) == 0 {
		return
	}
	p := s.Puller()
	for _, src := range cfg.Workspaces {
		if !p.Due(src) {
			continue
		}
		if res, err := p.Pull(ctx, src); err != nil {
			if !s.warned {
				s.logf("cartographer: auto-pull %s failed (using the snapshots already pulled; run `cartographer pull` to retry): %v", src, err)
				s.warned = true
			}
		} else if res.Imported {
			s.logf("cartographer: pulled %s from %s", res.Snapshot, src)
			s.forget()
		}
	}
}

// forget drops the cached choice and freshness (a new snapshot arrived).
func (s *Session) forget() {
	s.mu.Lock()
	s.memo = nil
	s.mu.Unlock()
}

// current returns the memo for s.Cwd, refreshing it after CacheTTL. A memo
// computed under a cancelled ctx (its git execs were killed) is returned
// but not kept. The caller holds s.mu.
func (s *Session) current(ctx context.Context) *memo {
	if m := s.memo; m != nil && m.cwd == s.Cwd && time.Since(m.at) < CacheTTL {
		return m
	}
	m := &memo{at: time.Now(), cwd: s.Cwd, co: fresh.Find(ctx, s.Cwd), reports: map[string]*fresh.Report{}}
	if ctx.Err() == nil {
		s.memo = m
	}
	return m
}

// resolve returns the snapshot to query and its freshness, with one
// repository discovery per cache period.
func (s *Session) resolve(ctx context.Context) (string, *fresh.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Value(noCacheKey{}) != nil {
		s.memo = nil
	}
	m := s.current(ctx)
	id := s.Snapshot
	if id == "" {
		if m.chosen == "" {
			var err error
			if m.chosen, err = s.chooseFrom(ctx, m.co); err != nil {
				return "", nil, err
			}
		}
		id = m.chosen
	}
	rep, err := s.freshFrom(ctx, m, id)
	return id, rep, err
}

// Query returns a query context with freshness applied.
func (s *Session) Query(ctx context.Context) (*query.Q, *fresh.Report, error) {
	if s.AutoPull {
		s.PullDue(ctx)
	}
	id, rep, err := s.resolve(ctx)
	if err != nil {
		return nil, nil, err
	}
	q, err := query.New(ctx, s.Store, id)
	if err != nil {
		return nil, nil, err
	}
	q.Changed = rep.Changed
	q.Staleness = rep.Summary()
	return q, rep, nil
}

// Fresh checks a snapshot against local checkouts (cached for CacheTTL).
func (s *Session) Fresh(ctx context.Context, id string) (*fresh.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freshFrom(ctx, s.current(ctx), id)
}

// freshFrom computes (or reuses) id's report. The caller holds s.mu.
func (s *Session) freshFrom(ctx context.Context, m *memo, id string) (*fresh.Report, error) {
	if rep, ok := m.reports[id]; ok {
		return rep, nil
	}
	repos, err := s.repos(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, _ := userconfig.Load()
	var explicit map[string]string
	if cfg != nil {
		explicit = cfg.Checkouts
	}
	rep := fresh.Check(ctx, repos, fresh.LocateFrom(ctx, repos, m.co, explicit))
	if ctx.Err() == nil { // a cancelled call's git execs failed; don't keep its answer
		m.reports[id] = rep
	}
	return rep, nil
}

func (s *Session) repos(ctx context.Context, id string) ([]fresh.Repo, error) {
	infos, err := s.Store.Repos(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]fresh.Repo, len(infos))
	for i, r := range infos {
		out[i] = fresh.Repo{Name: r.Name, URL: r.URL, Revision: r.Revision}
	}
	return out, nil
}

// ErrNotMapped means the current repository isn't covered by any imported map.
type ErrNotMapped struct{ Workspaces []string }

func (e *ErrNotMapped) Error() string {
	return fmt.Sprintf("this repository isn't covered by any pulled map (have: %s); pull its workspace with `cartographer pull <owner/name>` or pass --snapshot", strings.Join(e.Workspaces, ", "))
}

// choose picks the newest snapshot of the workspace whose repositories
// include the one at cwd. Outside any git repository it uses the only
// workspace, if there is exactly one. It never guesses across projects.
func (s *Session) choose(ctx context.Context) (string, error) {
	return s.chooseFrom(ctx, fresh.Find(ctx, s.Cwd))
}

func (s *Session) chooseFrom(ctx context.Context, co fresh.Checkout) (string, error) {
	all, err := s.Store.Snapshots(ctx)
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", store.ErrNoSnapshot
	}
	latest := map[string]string{} // workspace -> newest snapshot (all is newest first)
	var names []string
	for _, si := range all {
		if _, ok := latest[si.Workspace]; !ok {
			latest[si.Workspace] = si.ID
			names = append(names, si.Workspace)
		}
	}
	if co.Root == "" {
		if len(names) == 1 {
			return latest[names[0]], nil
		}
		return "", &ErrNotMapped{Workspaces: names}
	}
	// A [checkouts] path naming this checkout outranks everything (a fork's
	// origin matches no workspace URL); then an origin match anywhere
	// outranks a directory-name match, so a newer workspace with a
	// same-named repo can't shadow the right project.
	repos := make([][]fresh.Repo, len(names))
	for i, ws := range names {
		if repos[i], err = s.repos(ctx, latest[ws]); err != nil {
			return "", err
		}
	}
	if cfg, _ := userconfig.Load(); cfg != nil {
		for i, ws := range names {
			for _, r := range repos[i] {
				if p, ok := cfg.Checkouts[r.Name]; ok && samePath(fresh.ExpandHome(p), co.Root) {
					return latest[ws], nil
				}
			}
		}
	}
	for i, ws := range names {
		if co.MatchOrigin(repos[i]) != "" {
			return latest[ws], nil
		}
	}
	for i, ws := range names {
		if co.Match(repos[i]) != "" {
			return latest[ws], nil
		}
	}
	return "", &ErrNotMapped{Workspaces: names}
}

// Choose is the snapshot queries from cwd would use (see choose), cached
// for CacheTTL.
func (s *Session) Choose(ctx context.Context) (string, error) {
	if s.Snapshot != "" {
		return s.Snapshot, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.current(ctx)
	if m.chosen == "" {
		id, err := s.chooseFrom(ctx, m.co)
		if err != nil {
			return "", err
		}
		m.chosen = id
	}
	return m.chosen, nil
}

// Mapped reports whether cwd is inside a repository covered by any snapshot
// (two git execs).
func (s *Session) Mapped(ctx context.Context) bool {
	co := fresh.Find(ctx, s.Cwd)
	if co.Root == "" {
		return false
	}
	_, err := s.chooseFrom(ctx, co)
	return err == nil
}

// samePath reports whether a and b name the same directory, through symlinks
// (macOS temp dirs live under /var -> /private/var).
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
