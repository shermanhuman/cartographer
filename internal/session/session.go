// Package session prepares a query for the current directory: pull newer
// snapshots when due, pick the snapshot whose workspace covers the current
// repository, mark results against local changes, and start a local rebuild
// when a checkout has moved past the snapshot.
package session

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
	"github.com/shermanhuman/cartographer/internal/workspace"
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
	DB       string // the store's database file; a rebuild imported there counts as built
	Cwd      string
	Snapshot string // explicit snapshot id; "" selects by cwd
	AutoPull bool
	// Background pulls off the query path (long-lived processes); otherwise
	// the pull runs in the foreground within PullTimeout.
	Background bool
	Log        io.Writer // warnings (pull failures); nil discards
	GitHub     *snapshot.GitHub
	// Rebuild starts a local rebuild of the registered workspace at dir
	// (whose cartographer.toml names ws) when a checkout it builds is ahead
	// of the snapshot and no rebuild is running or recently failed
	// (rebuild.Dir in DataDir). It must return at once (spawn or go); the
	// caller holds the session lock. nil never rebuilds.
	Rebuild func(ws, dir string)
	// StateDir holds the registered projects (project.StateDir); "" means
	// none are considered.
	StateDir string
	// ProjectRebuild starts a cycle of a registered project when its last
	// fetch attempt is Cadence old or it was never attempted (and none is
	// running). It must return at once. nil never starts one.
	ProjectRebuild func(name string)
	// Now is the clock for project cadence (tests); nil is time.Now.
	Now func() time.Time

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
	local   map[string]string        // workspace name -> registered workspace dir ("" none)
	sibling map[string]string        // workspace name -> unregistered sibling workspace ("" none)
	// projects are the registered projects; scheduled is set once their
	// cycles were considered for this memo.
	projects  []*project.Registration
	scheduled bool
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

// Forget drops the cached choice and freshness (a new snapshot arrived).
func (s *Session) Forget() { s.forget() }

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
	m := &memo{at: time.Now(), cwd: s.Cwd, co: fresh.Find(ctx, s.Cwd), reports: map[string]*fresh.Report{}, local: map[string]string{}, sibling: map[string]string{}}
	m.projects = s.registrations()
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
	s.scheduleProjects(m)
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
	q.Title = rep.Title
	q.Notes = rep.Hints
	return q, rep, nil
}

// Fresh checks a snapshot against local checkouts (cached for CacheTTL).
func (s *Session) Fresh(ctx context.Context, id string) (*fresh.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.current(ctx)
	s.scheduleProjects(m)
	return s.freshFrom(ctx, m, id)
}

func (s *Session) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// registrations lists the projects registered with this session's store
// (none without StateDir). A project registered with another store is
// neither scheduled nor applied here: its builds go only to its own store.
func (s *Session) registrations() []*project.Registration {
	regs, err := project.List(s.StateDir)
	if err != nil {
		s.logf("cartographer: %v", err)
	}
	var out []*project.Registration
	for _, r := range regs {
		if r.InStore(s.DB) {
			out = append(out, r)
		}
	}
	return out
}

func registered(regs []*project.Registration, ws string) *project.Registration {
	for _, r := range regs {
		if r.Name == ws {
			return r
		}
	}
	return nil
}

// scheduleProjects starts a cycle of each registered project that is due
// (never attempted, or its last fetch attempt is project.Cadence old) and
// not running, once per memo. The cycle itself decides, under the lock,
// whether anything changed. The caller holds s.mu.
func (s *Session) scheduleProjects(m *memo) {
	if m.scheduled || s.ProjectRebuild == nil || s.Snapshot != "" {
		return
	}
	m.scheduled = true
	now := s.now()
	for _, reg := range m.projects {
		st, err := project.LoadState(s.StateDir, reg.Name)
		if err != nil || !st.Due(now) {
			continue
		}
		if _, running := project.Lock(s.StateDir, reg.Name).Running(reg.Name); running {
			continue
		}
		s.ProjectRebuild(reg.Name)
	}
}

// memberOf names the registered member a checkout holds: by its recorded
// path, else by any remote naming the member's GitHub URL ("" none).
func memberOf(ctx context.Context, reg *project.Registration, co fresh.Checkout) string {
	if co.Root == "" {
		return ""
	}
	for _, mb := range reg.Members {
		if samePath(mb.Path, co.Root) {
			return mb.Name()
		}
	}
	if slug := project.Identify(ctx, co.Root); slug != "" {
		if mb, ok := reg.Member(slug); ok {
			return mb.Name()
		}
	}
	return ""
}

// projectReport is the freshness of a registered project's snapshot: the
// recorded checkouts, except that the one the command runs in (a worktree,
// a second clone) stands for its member, with the header and status
// describing the map, the fetch and your checkout.
func (s *Session) projectReport(ctx context.Context, m *memo, id string, reg *project.Registration, repos []fresh.Repo) *fresh.Report {
	paths := map[string]string{}
	for _, mb := range reg.Members {
		if mb.Path != "" {
			paths[mb.Name()] = mb.Path
		}
	}
	yours := memberOf(ctx, reg, m.co)
	if yours != "" {
		paths[yours] = m.co.Root
	}
	rep := fresh.CheckWork(ctx, repos, paths)
	st, err := project.LoadState(s.StateDir, reg.Name)
	if err != nil {
		st = &project.State{}
	}
	v := &project.View{Reg: reg, State: st, Snapshot: id, Report: rep, Yours: yours, Now: s.now()}
	if cfg, _ := userconfig.Load(); cfg != nil {
		v.Roots = cfg.Roots()
	}
	if since, ok := project.Lock(s.StateDir, reg.Name).Running(reg.Name); ok {
		v.Running = since
	}
	if si, ok, _ := s.Store.Info(ctx, id); ok {
		v.MapID, v.Source = si.MapID, si.Source
		v.Created, _ = time.Parse(time.RFC3339, si.CreatedAt)
	}
	v.Manifest, _ = s.Store.Manifest(ctx, id)
	rep.Lead, rep.Detail, rep.Title = v.Lead(), v.Detail(), v.Label()
	return rep
}

// ErrFirstBuild means a registered project has no map yet.
type ErrFirstBuild struct {
	Project string
	Status  string
}

func (e *ErrFirstBuild) Error() string {
	return fmt.Sprintf("no map of project %s yet: %s", e.Project, e.Status)
}

func (s *Session) firstBuild(reg *project.Registration) error {
	status := "its first build starts with the next command, or run `cartographer refresh`"
	if since, ok := project.Lock(s.StateDir, reg.Name).Running(reg.Name); ok {
		status = fmt.Sprintf("its first build is running since %s (log: %s)", since.Local().Format("15:04"), project.LogPath(s.StateDir, reg.Name))
	} else if st, err := project.LoadState(s.StateDir, reg.Name); err == nil && st.Failed != nil {
		v := &project.View{Reg: reg, State: st, Now: s.now()}
		status = strings.Join(v.Problems(), "; ")
	}
	return &ErrFirstBuild{Project: reg.Name, Status: status}
}

// freshFrom computes (or reuses) id's report, and with it decides once per
// computation whether to start a rebuild (see rebuildDue). The caller holds
// s.mu.
func (s *Session) freshFrom(ctx context.Context, m *memo, id string) (*fresh.Report, error) {
	if rep, ok := m.reports[id]; ok {
		return rep, nil
	}
	repos, err := s.repos(ctx, id)
	if err != nil {
		return nil, err
	}
	if ws, _ := s.Store.Workspace(ctx, id); ws != "" {
		if reg := registered(m.projects, ws); reg != nil {
			// A registered project: never the HEAD-based trigger, and a
			// legacy workspace of the same name is ignored.
			rep := s.projectReport(ctx, m, id, reg, repos)
			if ctx.Err() == nil {
				m.reports[id] = rep
			}
			return rep, nil
		}
		if si, ok, _ := s.Store.Info(ctx, id); ok && si.Source == "head" {
			// A `refresh --head` map of an unregistered project: labelled
			// "your checkouts" all the same, its checkouts found as usual.
			cfg, _ := userconfig.Load()
			if cfg == nil {
				cfg = &userconfig.Config{}
			}
			reg := &project.Registration{Name: ws}
			for name, p := range fresh.LocateFrom(ctx, repos, m.co, cfg.Checkouts) {
				for _, r := range repos {
					if r.Name == name {
						slug := strings.TrimPrefix(fresh.NormalizeURL(r.URL), "github.com/")
						reg.Members = append(reg.Members, project.Member{Repo: slug, Path: p})
					}
				}
			}
			rep := s.projectReport(ctx, m, id, reg, repos)
			if ctx.Err() == nil {
				m.reports[id] = rep
			}
			return rep, nil
		}
	}
	cfg, _ := userconfig.Load()
	if cfg == nil {
		cfg = &userconfig.Config{}
	}
	rep := fresh.Check(ctx, repos, fresh.LocateFrom(ctx, repos, m.co, cfg.Checkouts))
	if s.DataDir != "" {
		if ws, _ := s.Store.Workspace(ctx, id); ws != "" {
			rep.Rebuild = rebuild.Dir{Path: s.DataDir}.Status(ws)
			if s.Rebuild != nil {
				s.rebuildDue(ctx, m, ws, repos, rep, cfg)
			}
		}
	}
	if ctx.Err() == nil { // a cancelled call's git execs failed; don't keep its answer
		m.reports[id] = rep
	}
	return rep, nil
}

// recentlyBuilt reports whether a rebuild imported exactly heads into db
// within FailBackoff: a guard against a tight loop when a newer snapshot
// at older revisions (a pull, an import) is in use, which a later rebuild
// may still replace.
func recentlyBuilt(d rebuild.Dir, ws, db string, heads map[string]string) (time.Time, bool) {
	revs, at := d.Built(ws, db)
	return at, revs != nil && maps.Equal(revs, heads) && time.Since(at) < rebuild.FailBackoff
}

// rebuildDue starts a rebuild of ws when the report shows a checkout ahead
// of the snapshot and the checkouts the registered workspace builds (the
// ones workspace.Local resolves, which may not be the one the query runs
// in: a second clone, a worktree) are ahead too, so the new snapshot will
// answer the evidence. It never starts one for a workspace whose
// cartographer.toml changed since it was registered (userconfig.Trust), for
// a pinned snapshot, while one runs, within FailBackoff of a failure, or
// right after a rebuild of the same HEADs. A workspace that can't be
// resolved still starts one:
// the build records why, and that backs off. Advice goes to rep.Hints.
func (s *Session) rebuildDue(ctx context.Context, m *memo, ws string, repos []fresh.Repo, rep *fresh.Report, cfg *userconfig.Config) {
	dir, ok := m.local[ws]
	if !ok {
		dir = rebuild.Registered(ws, cfg.LocalWorkspaces)
		m.local[ws] = dir
	}
	if dir == "" {
		sib, ok := m.sibling[ws]
		if !ok {
			sib = rebuild.Sibling(ws, m.co)
			m.sibling[ws] = sib
		}
		if sib != "" {
			rep.Hints = append(rep.Hints, "to keep this map fresh automatically: cartographer workspace add "+sib)
		}
		return
	}
	switch userconfig.Trust(dir) {
	case userconfig.TrustChanged:
		rep.Hints = append(rep.Hints, "cartographer.toml changed since you registered it: review, then cartographer workspace add "+dir)
		return
	case userconfig.TrustUnknown: // listed by hand, never added
		rep.Hints = append(rep.Hints, "to keep this map fresh automatically: cartographer workspace add "+dir)
		return
	}
	d := rebuild.Dir{Path: s.DataDir}
	if s.Snapshot != "" || !rebuild.Needed(rep) || !d.Due(ws) {
		return
	}
	if _, running := d.Running(ws); running {
		return
	}
	wcfg, err := workspace.Load(dir)
	var paths map[string]string
	if err == nil {
		paths, _, err = workspace.Local(ctx, wcfg, cfg.Checkouts)
	}
	if err != nil {
		s.Rebuild(ws, dir)
		return
	}
	snap := map[string]string{}
	for _, r := range repos {
		snap[r.Name] = r.Revision
	}
	heads := map[string]string{}
	ahead := false
	for name, p := range paths {
		heads[name] = fresh.Head(ctx, p)
		if rev, ok := snap[name]; !ok || heads[name] != rev && fresh.Ahead(ctx, p, rev) {
			ahead = true
		}
	}
	at, recent := recentlyBuilt(d, ws, s.DB, heads)
	switch {
	case !ahead:
		for _, st := range rep.Repos {
			if (st.Behind > 0 || st.Note == fresh.NoteRevisionMissing) && paths[st.Name] != "" && !samePath(paths[st.Name], st.Path) {
				rep.Hints = append(rep.Hints, fmt.Sprintf("checkout here differs from the one the workspace builds: %s at %s", st.Name, paths[st.Name]))
			}
		}
	case recent:
		rep.Hints = append(rep.Hints, fmt.Sprintf("rebuilt these checkouts at %s, but a newer snapshot came in since; rebuilds again after %s", at.Local().Format("15:04"), at.Add(rebuild.FailBackoff).Local().Format("15:04")))
	default:
		s.Rebuild(ws, dir)
	}
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
	return fmt.Sprintf("this repository isn't covered by any map (have: %s); build its workspace with `cartographer build --local --import` there (and `cartographer workspace add <dir>` to keep it fresh), pull it with `cartographer pull <owner/name>`, or pass --snapshot", strings.Join(e.Workspaces, ", "))
}

// choose picks the newest snapshot of the workspace whose repositories
// include the one at cwd. Outside any git repository it uses the only
// workspace, if there is exactly one. It never guesses across projects.
func (s *Session) choose(ctx context.Context) (string, error) {
	return s.chooseFrom(ctx, fresh.Find(ctx, s.Cwd))
}

func (s *Session) chooseFrom(ctx context.Context, co fresh.Checkout) (string, error) {
	id, _, err := s.chooseRepo(ctx, co)
	return id, err
}

// chooseRepo is chooseFrom that also names the workspace repository the
// checkout matched ("" outside git).
func (s *Session) chooseRepo(ctx context.Context, co fresh.Checkout) (string, string, error) {
	all, err := s.Store.Snapshots(ctx)
	if err != nil {
		return "", "", err
	}
	regs := s.registrations()
	if len(all) == 0 {
		if len(regs) > 0 {
			reg := regs[0]
			for _, r := range regs {
				if memberOf(ctx, r, co) != "" {
					reg = r
				}
			}
			return "", "", s.firstBuild(reg)
		}
		return "", "", store.ErrNoSnapshot
	}
	latest := map[string]string{} // workspace -> newest snapshot (all is newest first)
	var names []string
	for _, si := range all {
		// A registered project's map comes only from its fetched mains
		// (never a `head` or published snapshot of the same name).
		if registered(regs, si.Workspace) != nil && si.Source != "main" {
			continue
		}
		if _, ok := latest[si.Workspace]; !ok {
			latest[si.Workspace] = si.ID
			names = append(names, si.Workspace)
		}
	}
	// The checkout is a registered project's member (by recorded path or
	// remote): that project, or its first build.
	for _, reg := range regs {
		if name := memberOf(ctx, reg, co); name != "" {
			if id, ok := latest[reg.Name]; ok {
				return id, name, nil
			}
			return "", "", s.firstBuild(reg)
		}
	}
	if co.Root == "" {
		if len(names) == 1 {
			return latest[names[0]], "", nil
		}
		if len(names) == 0 && len(regs) > 0 {
			return "", "", s.firstBuild(regs[0])
		}
		return "", "", &ErrNotMapped{Workspaces: names}
	}
	// Evidence ranks origin, then a [checkouts] path, then directory name.
	// An origin match anywhere wins, so a newer workspace with a same-named
	// repo can't shadow the right project; a [checkouts] path (keyed by repo
	// name, so it can't tell such workspaces apart) maps a fork, whose origin
	// matches no workspace URL; the name is the last resort.
	repos := make([][]fresh.Repo, len(names))
	for i, ws := range names {
		if repos[i], err = s.repos(ctx, latest[ws]); err != nil {
			return "", "", err
		}
	}
	for i, ws := range names {
		if name := co.MatchOrigin(repos[i]); name != "" {
			return latest[ws], name, nil
		}
	}
	if cfg, _ := userconfig.Load(); cfg != nil {
		for i, ws := range names {
			for _, r := range repos[i] {
				if p, ok := cfg.Checkouts[r.Name]; ok && samePath(fresh.ExpandHome(p), co.Root) {
					return latest[ws], r.Name, nil
				}
			}
		}
	}
	for i, ws := range names {
		if name := co.Match(repos[i]); name != "" {
			return latest[ws], name, nil
		}
	}
	return "", "", &ErrNotMapped{Workspaces: names}
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

// HasCode reports whether cwd is inside a mapped repository that has at
// least one module or function in the snapshot chosen for it. A docs-only
// repository (no extractor plugins) is mapped but has no code.
func (s *Session) HasCode(ctx context.Context) bool {
	co := fresh.Find(ctx, s.Cwd)
	if co.Root == "" {
		return false
	}
	id, repo, err := s.chooseRepo(ctx, co)
	if err != nil || repo == "" {
		return false
	}
	ok, err := s.Store.RepoHasCode(ctx, id, repo)
	return err == nil && ok
}

// samePath reports whether a and b name the same directory, through symlinks
// (macOS temp dirs live under /var -> /private/var).
func samePath(a, b string) bool { return project.SamePath(a, b) }
