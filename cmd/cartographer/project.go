package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
	"github.com/shermanhuman/cartographer/internal/workspace"
	"github.com/shermanhuman/cartographer/plugins"
)

// stdin is where project add reads its confirmation (tests replace it).
var stdin io.Reader = os.Stdin

// newRunner sets up project cycles over the open store s.
func newRunner(c *common, s *store.Store, log io.Writer) (*project.Runner, error) {
	ucfg, err := userconfig.Load()
	if err != nil {
		return nil, err
	}
	pdir := c.plugins
	if pdir == "" {
		if pdir, err = plugins.Extract(workspace.DefaultCacheDir()); err != nil {
			return nil, err
		}
	}
	pdigest, err := workspace.DirDigest(pdir)
	if err != nil {
		return nil, fmt.Errorf("plugins digest: %w", err)
	}
	db, _ := filepath.Abs(c.db)
	sd := project.StateDir()
	if err := os.MkdirAll(sd, 0o755); err != nil {
		return nil, err
	}
	return &project.Runner{
		StateDir: sd, Store: s, DB: db,
		PluginsDir: pdir, PluginsDigest: pdigest, CacheDir: workspace.DefaultCacheDir(),
		Version: version, Mise: &project.Mise{Dir: sd, Log: log},
		Checkouts: ucfg.Checkouts, Roots: ucfg.Roots(), Log: log,
	}, nil
}

// spawnProject is the Session.ProjectRebuild of a one-shot command: a
// detached `cartographer refresh <name> --background`, which takes the
// project's lock, re-reads its state and logs to the project's log.
func spawnProject(c *common, log io.Writer) func(name string) {
	return func(name string) {
		err := func() error {
			exe, err := selfExe()
			if err != nil {
				return err
			}
			db, err := filepath.Abs(c.db)
			if err != nil {
				return err
			}
			args := []string{"refresh", name, "--background", "--db", db}
			if c.plugins != "" {
				args = append(args, "--plugins", c.plugins)
			}
			cmd := exec.Command(exe, args...)
			cmd.Dir = project.StateDir()
			os.MkdirAll(cmd.Dir, 0o755)
			detach(cmd)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		}()
		if err != nil && log != nil {
			fmt.Fprintf(log, "cartographer: could not start the refresh of project %s: %v\n", name, err)
		}
	}
}

// backgroundProject is the Session.ProjectRebuild of a long-lived process
// (mcp, serve): a cycle in a goroutine, importing into the open store.
func backgroundProject(ctx context.Context, c *common, sess *session.Session, s *store.Store) (start func(name string), wait func()) {
	var wg sync.WaitGroup
	start = func(name string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := refreshOne(ctx, io.Discard, c, s, name, true)
			switch {
			case errors.Is(err, rebuild.ErrRunning), errors.Is(err, rebuild.ErrLost), errors.Is(err, context.Canceled):
			case err != nil:
				if sess.Log != nil {
					fmt.Fprintf(sess.Log, "cartographer: refresh of project %s failed (log: %s): %v\n", name, project.LogPath(project.StateDir(), name), err)
				}
			case res.Imported:
				sess.Forget()
			}
		}()
	}
	wait = func() {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(shutdownWait):
		}
	}
	return start, wait
}

// refreshOne runs one cycle of project name. background is an automatic
// cycle (cadence and the failed-digest wait apply, output only to the log);
// otherwise it is `cartographer refresh`, also printing to stderr.
func refreshOne(ctx context.Context, w io.Writer, c *common, s *store.Store, name string, background bool) (project.Result, error) {
	sd := project.StateDir()
	if _, err := project.Load(sd, name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return project.Result{}, fmt.Errorf("project %s is not registered (cartographer project add <checkout>)", name)
		}
		return project.Result{}, err
	}
	lock := project.Lock(sd, name)
	if since, running := lock.Running(name); running {
		if background {
			return project.Result{}, rebuild.ErrRunning
		}
		return project.Result{}, fmt.Errorf("a refresh of %s is already running since %s (log: %s)", name, since.Local().Format("15:04:05"), project.LogPath(sd, name))
	}
	if err := os.MkdirAll(project.Dir(sd, name), 0o755); err != nil {
		return project.Result{}, err
	}
	// The lock makes the cycle the log's only writer; a cycle that finds
	// the lock taken leaves it alone.
	var logw io.Writer = io.Discard
	var f *os.File
	open := func() io.Writer {
		if f == nil {
			var err error
			if f, err = os.Create(project.LogPath(sd, name)); err == nil {
				logw = f
			}
		}
		if background {
			return logw
		}
		return io.MultiWriter(logw, os.Stderr)
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	r, err := newRunner(c, s, nil)
	if err != nil {
		return project.Result{}, err
	}
	r.Log = &lazyWriter{open: open}
	r.Mise.Log = r.Log
	res, err := r.Cycle(ctx, name, !background)
	if err != nil && !errors.Is(err, rebuild.ErrRunning) {
		fmt.Fprintln(r.Log, "cartographer:", err)
	}
	if err != nil && errors.Is(err, rebuild.ErrRunning) && !background {
		return res, fmt.Errorf("a refresh of %s is already running (log: %s)", name, project.LogPath(sd, name))
	}
	return res, err
}

// lazyWriter opens the log on the first write (under the lock).
type lazyWriter struct {
	mu   sync.Mutex
	open func() io.Writer
	w    io.Writer
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		l.w = l.open()
	}
	return l.w.Write(p)
}

// refreshCmd is `cartographer refresh [name] [--head --from <dir>]`.
func refreshCmd(ctx context.Context, w io.Writer, c *common, pos []string, dbSet bool) error {
	ctx, stop := withSignals(ctx)
	defer stop()
	s, err := openStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	if c.head {
		if c.from == "" {
			return errors.New("refresh --head: --from <member checkout> names the working copies to build")
		}
		if !dbSet {
			return errors.New("refresh --head builds your working copies: give it its own --db, so the map built from main stays as it is")
		}
		if err := headStoreAllowed(c.db); err != nil {
			return err
		}
		r, err := newRunner(c, s, os.Stderr)
		if err != nil {
			return err
		}
		res, err := r.Head(ctx, c.from)
		if err != nil {
			return err
		}
		return printResult(ctx, w, s, "your checkouts", res)
	}
	if c.from != "" {
		return errors.New("refresh: --from goes with --head")
	}
	sd := project.StateDir()
	var names []string
	if len(pos) > 0 {
		names = pos
	} else {
		all, err := project.List(sd)
		if err != nil {
			return err
		}
		// Only the projects that build into this store.
		db, _ := filepath.Abs(c.db)
		var regs []*project.Registration
		for _, reg := range all {
			if reg.InStore(db) {
				regs = append(regs, reg)
			}
		}
		if len(regs) == 0 && len(all) > 0 {
			return fmt.Errorf("no project builds into %s (each builds into the store it was added with; see `cartographer project list`)", db)
		}
		cwd, _ := os.Getwd()
		co := fresh.Find(ctx, cwd)
		for _, reg := range regs {
			for _, m := range reg.Members {
				if co.Root != "" && (m.Path == co.Root || strings.EqualFold(project.Identify(ctx, co.Root), m.Repo)) {
					names = []string{reg.Name}
				}
			}
		}
		if len(names) == 0 {
			for _, reg := range regs {
				names = append(names, reg.Name)
			}
		}
		if len(names) == 0 {
			return errors.New("no project registered: cartographer project add <member checkout | owner/name>")
		}
	}
	var failed []error
	for _, name := range names {
		res, err := refreshOne(ctx, w, c, s, name, c.background)
		if c.background {
			if err != nil && !errors.Is(err, rebuild.ErrRunning) {
				failed = append(failed, err)
			}
			continue
		}
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := printResult(ctx, w, s, name, res); err != nil {
			return err
		}
	}
	return errors.Join(failed...)
}

// headStoreAllowed refuses a `refresh --head` into the default store or into
// any registered project's store, so a working-copy map never lands beside
// (or prunes) the maps built from main.
func headStoreAllowed(db string) error {
	abs, err := filepath.Abs(db)
	if err != nil {
		return err
	}
	if project.SamePath(abs, filepath.Join(session.DataDir(), "carto.db")) {
		return fmt.Errorf("refresh --head: %s is the default store; give it a scratch --db, so the map built from main stays as it is", abs)
	}
	regs, _ := project.List(project.StateDir())
	for _, reg := range regs {
		if reg.InStore(abs) {
			return fmt.Errorf("refresh --head: %s is project %s's store; give it a scratch --db, so the map built from main stays as it is", abs, reg.Name)
		}
	}
	return nil
}

func printResult(ctx context.Context, w io.Writer, s *store.Store, name string, res project.Result) error {
	if !res.Imported {
		fmt.Fprintf(w, "%s: nothing to build (%s)\n", name, res.Skipped)
		return nil
	}
	si, _, err := s.Info(ctx, res.Snapshot)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: imported map %s (snapshot %s)\n", name, si.MapID[:min(8, len(si.MapID))], res.Snapshot)
	return nil
}

// projectCmd is `cartographer project add|remove|list|init`.
func projectCmd(ctx context.Context, w io.Writer, c *common, pos []string) error {
	usage := errors.New("usage: cartographer project add <member checkout | owner/name> [--yes] | remove <name> | list | init <checkout> [--project NAME --anchor owner/name]")
	if len(pos) == 0 {
		return usage
	}
	sd := project.StateDir()
	switch pos[0] {
	case "list":
		regs, err := project.List(sd)
		if err != nil {
			fmt.Fprintln(w, "warning:", err)
		}
		if len(regs) == 0 {
			fmt.Fprintln(w, "no projects registered")
		}
		for _, reg := range regs {
			fmt.Fprintf(w, "%s (anchor %s)\n", reg.Name, reg.Anchor)
			for _, m := range reg.Members {
				p := m.Path
				if p == "" {
					p = "no checkout"
				}
				fmt.Fprintf(w, "  %s  %s\n", m.Repo, p)
			}
			fmt.Fprintf(w, "  store %s\n", reg.Store)
			if st, err := project.LoadState(sd, reg.Name); err == nil {
				switch {
				case st.Attempt.IsZero():
					fmt.Fprintln(w, "  never built")
				case st.Failed != nil:
					fmt.Fprintf(w, "  last cycle %s; failed: %s %s: %s\n", st.Attempt.Local().Format("Jan 2 15:04"), st.Failed.Repo, st.Failed.Step, st.Failed.Message)
				default:
					fmt.Fprintf(w, "  last cycle %s\n", st.Attempt.Local().Format("Jan 2 15:04"))
				}
			}
		}
		return nil
	case "remove":
		if len(pos) < 2 {
			return usage
		}
		if err := project.Remove(sd, pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed project %s; its maps stay in the store until pruned, and refs/cartographer/* stay in its checkouts\n", pos[1])
		return nil
	case "init":
		if len(pos) < 2 {
			return usage
		}
		name, anchor := c.projectName, c.anchor
		if name == "" {
			slug := project.Identify(ctx, pos[1])
			regs, _ := project.List(sd)
			for _, reg := range regs {
				if _, ok := reg.Member(slug); ok && slug != "" {
					name, anchor = reg.Name, reg.Anchor
				}
			}
		}
		if name == "" {
			return errors.New("project init: this checkout isn't a member of a registered project; say which with --project NAME [--anchor owner/name]")
		}
		file, cmds, err := project.Init(ctx, pos[1], name, anchor)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s (check extract and docs). Nothing is committed; to commit it on a new branch:\n", file)
		for _, l := range cmds {
			fmt.Fprintln(w, "  "+l)
		}
		fmt.Fprintln(w, "then merge it into the default branch; the next build reads it from there.")
		return nil
	case "add":
		if len(pos) < 2 {
			return usage
		}
		s, err := openStore(c)
		if err != nil {
			return err
		}
		defer s.Close()
		r, err := newRunner(c, s, nil)
		if err != nil {
			return err
		}
		plan, err := r.Plan(ctx, pos[1])
		if err != nil {
			return err
		}
		reg := plan.Reg
		fmt.Fprintf(w, "project %s, anchor %s:\n", reg.Name, reg.Anchor)
		for _, m := range reg.Members {
			p := m.Path
			if p == "" {
				p = "no checkout"
			}
			branch := m.Branch
			if branch == "" {
				branch = "default branch"
			}
			fmt.Fprintf(w, "  %-40s %s (%s)\n", m.Repo, p, branch)
		}
		for _, cmd := range plan.Missing {
			fmt.Fprintf(w, "  missing; builds fail until it is cloned: %s\n", cmd)
		}
		fmt.Fprintf(w, `Registering trusts these repos' fetched default branches: rebuilds run code from them
(the schema step, rust-analyzer's build scripts and proc macros, and the commands their
.cartographer/repo.toml declares), and a repo added to %s's member list later runs
without another prompt. Nothing is written into the checkouts but refs/cartographer/*.
`, reg.Anchor)
		if !c.yes {
			fmt.Fprintf(w, "Register project %s? [y/N] ", reg.Name)
			line, _ := bufio.NewReader(stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				return errors.New("not registered")
			}
		}
		if err := project.Save(sd, reg); err != nil {
			return err
		}
		fmt.Fprintf(w, "registered %s in %s; the next command builds its map in the background, or run `cartographer refresh` now\n", reg.Name, sd)
		return nil
	}
	return usage
}

// reviewMember is `review <id>` in a project member checkout: it stamps
// the annotation in the checkout's .cartographer/annotations against the
// map chosen for it.
func reviewMember(ctx context.Context, w io.Writer, c *common, id string) error {
	cwd, _ := os.Getwd()
	root := fresh.Root(ctx, cwd)
	if root == "" {
		return fmt.Errorf("review: no %s here and not in a checkout", workspace.FileName)
	}
	dir := filepath.Join(root, filepath.FromSlash(project.AnnotationsDir))
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("review: %s has no %s (and there is no %s here)", root, project.AnnotationsDir, workspace.FileName)
	}
	s, err := openStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	sess := newSession(c, s, io.Discard)
	sess.ProjectRebuild, sess.Rebuild = nil, nil
	sid, err := sess.Choose(ctx)
	if err != nil {
		return err
	}
	n, err := workspace.ReviewDir(ctx, dir, s.DB, sid, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "reviewed %s against %s: %d citations stamped; commit %s\n", id, sid, n, project.AnnotationsDir)
	return nil
}
