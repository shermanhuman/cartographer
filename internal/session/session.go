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

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
)

type Session struct {
	Store    *store.Store
	DataDir  string
	Cwd      string
	Snapshot string // explicit snapshot id; "" selects by cwd
	AutoPull bool
	Log      io.Writer // warnings (pull failures); nil discards
	GitHub   *snapshot.GitHub

	warned bool
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
	gh := s.GitHub
	if gh == nil {
		gh = snapshot.NewGitHub()
		s.GitHub = gh
	}
	return &snapshot.Puller{Store: s.Store, GitHub: gh, DataDir: s.DataDir}
}

// PullDue pulls every configured workspace whose last check is older than
// snapshot.AutoInterval. Failures are reported once and never fatal.
func (s *Session) PullDue(ctx context.Context) {
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
				s.logf("cartographer: auto-pull %s failed: %v", src, err)
				s.warned = true
			}
		} else if res.Imported {
			s.logf("cartographer: pulled %s from %s", res.Snapshot, src)
		}
	}
}

// Query returns a query context with freshness applied.
func (s *Session) Query(ctx context.Context) (*query.Q, *fresh.Report, error) {
	if s.AutoPull {
		s.PullDue(ctx)
	}
	id := s.Snapshot
	if id == "" {
		var err error
		if id, err = s.choose(ctx); err != nil {
			return nil, nil, err
		}
	}
	q, err := query.New(ctx, s.Store, id)
	if err != nil {
		return nil, nil, err
	}
	rep, err := s.Fresh(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	q.Changed = rep.Changed
	q.Staleness = rep.Summary()
	return q, rep, nil
}

// Fresh checks a snapshot against local checkouts.
func (s *Session) Fresh(ctx context.Context, id string) (*fresh.Report, error) {
	repos, err := s.repos(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, _ := userconfig.Load()
	var explicit map[string]string
	if cfg != nil {
		explicit = cfg.Checkouts
	}
	return fresh.Check(ctx, repos, fresh.Locate(ctx, repos, s.Cwd, explicit)), nil
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
	root := fresh.Root(ctx, s.Cwd)
	if root == "" {
		if len(names) == 1 {
			return latest[names[0]], nil
		}
		return "", &ErrNotMapped{Workspaces: names}
	}
	for _, ws := range names {
		repos, err := s.repos(ctx, latest[ws])
		if err != nil {
			return "", err
		}
		for _, p := range fresh.Locate(ctx, repos, s.Cwd, nil) {
			if p == root {
				return latest[ws], nil
			}
		}
	}
	return "", &ErrNotMapped{Workspaces: names}
}

// Choose is the snapshot queries from cwd would use (see choose).
func (s *Session) Choose(ctx context.Context) (string, error) {
	if s.Snapshot != "" {
		return s.Snapshot, nil
	}
	return s.choose(ctx)
}

// Mapped reports whether cwd is inside a repository covered by any snapshot.
func (s *Session) Mapped(ctx context.Context) bool {
	id, err := s.choose(ctx)
	if err != nil {
		return false
	}
	repos, err := s.repos(ctx, id)
	if err != nil {
		return false
	}
	root := fresh.Root(ctx, s.Cwd)
	for _, p := range fresh.Locate(ctx, repos, s.Cwd, nil) {
		if p == root {
			return true
		}
	}
	return false
}
