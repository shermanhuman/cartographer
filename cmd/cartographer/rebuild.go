package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
	"github.com/shermanhuman/cartographer/internal/workspace"
	"github.com/shermanhuman/cartographer/plugins"
)

// selfExe locates the binary a one-shot command re-runs as the detached
// rebuild child (replaced in tests).
var selfExe = os.Executable

// shutdownWait bounds how long mcp/serve wait on exit for a cancelled
// rebuild to clean up.
var shutdownWait = 10 * time.Second

// rebuildEnabled: local rebuilds are on unless --no-rebuild,
// $CARTOGRAPHER_NO_REBUILD or `auto_rebuild = false` in the user config.
// A config that doesn't load turns them off; entries Load dropped are
// reported (Warnings) and the rest applies. Problems go to log.
func rebuildEnabled(c *common, log io.Writer) bool {
	if c.noRebuild {
		return false
	}
	cfg, err := userconfig.Load()
	if err != nil {
		if log != nil {
			fmt.Fprintln(log, "cartographer: local rebuilds off:", err)
		}
		return false
	}
	for _, w := range cfg.Warnings {
		if log != nil {
			fmt.Fprintln(log, "cartographer:", w)
		}
	}
	return cfg.AutoRebuildOn()
}

// withSignals cancels ctx on SIGINT/SIGTERM, so commands that run plugins
// (in their own process groups) or rebuild in the background stop them and
// clean up before exiting. A second signal exits at once, killing the
// plugins' process groups first (they don't get the terminal's signals).
func withSignals(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-done:
			return
		}
		select {
		case sig := <-sigs:
			workspace.KillGroups()
			code := 130 // 128 + SIGINT
			if sig == syscall.SIGTERM {
				code = 143
			}
			os.Exit(code)
		case <-done:
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() { signal.Stop(sigs); close(done) })
		cancel()
	}
}

// spawnRebuild is the Session.Rebuild of a one-shot command: it starts
// `cartographer build --local --import --background` detached and returns
// at once. The child takes the lock, opens the log and records the outcome
// itself, so two commands racing here build once and never share a log. A
// child that can't start is reported to log and recorded as a failure, so
// it backs off and status shows it.
func spawnRebuild(c *common, log io.Writer) func(ws, dir string) {
	return func(ws, dir string) {
		err := func() error {
			if !filepath.IsAbs(dir) { // registered entries are absolute (userconfig.Load)
				return fmt.Errorf("workspace path %q is not absolute", dir)
			}
			exe, err := selfExe()
			if err != nil {
				return err
			}
			db, err := filepath.Abs(c.db)
			if err != nil {
				return err
			}
			args := []string{"build", "--local", "--import", "--background", "--workspace", dir, "--db", db}
			if c.plugins != "" {
				args = append(args, "--plugins", c.plugins)
			}
			cmd := exec.Command(exe, args...)
			cmd.Dir = dir // stdin, stdout and stderr: the null device
			detach(cmd)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		}()
		if err != nil {
			err = fmt.Errorf("could not start the rebuild: %w", err)
			rebuild.Dir{Path: filepath.Dir(c.db)}.Fail(ws, err)
			if log != nil {
				fmt.Fprintf(log, "cartographer: local rebuild of %s: %v\n", ws, err)
			}
		}
	}
}

// backgroundRebuild returns the Session.Rebuild of a long-lived process
// (mcp, serve), which builds in a goroutine bounded by rebuild.Timeout and
// by ctx, importing into the open store; the session forgets its cached
// choice when a new snapshot lands. wait, called after ctx is cancelled on
// shutdown, lets a running build stop and release its lock and scratch.
func backgroundRebuild(ctx context.Context, c *common, sess *session.Session, s *store.Store) (start func(ws, dir string), wait func()) {
	var wg sync.WaitGroup
	start = func(ws, dir string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cc := *c
			cc.local, cc.doImport, cc.background, cc.workspace, cc.out = true, true, true, dir, ""
			err := localBuild(ctx, io.Discard, &cc, s)
			switch {
			case errors.Is(err, rebuild.ErrRunning), errors.Is(err, rebuild.ErrLost), errors.Is(err, context.Canceled):
			case err != nil:
				if sess.Log != nil {
					fmt.Fprintf(sess.Log, "cartographer: local rebuild of %s failed (log: %s): %v\n", ws, rebuild.Dir{Path: filepath.Dir(c.db)}.LogPath(ws), err)
				}
			default:
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

// localBuild is `build --local`: every checkout at its HEAD (no fetch, no
// token), all of it under the workspace's rebuild lock, so a failure at any
// step (a checkout missing, a plugin) is stamped, backs off and shows in
// status. With --background (an automatic rebuild) it is bounded by
// rebuild.Timeout and the output goes to the rebuild log, opened once the
// lock is held. s is an open store to import into (nil opens --db).
func localBuild(ctx context.Context, w io.Writer, c *common, s *store.Store) error {
	if c.background {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rebuild.Timeout)
		defer cancel()
	}
	name := workspace.Name(c.workspace)
	if name == "" {
		if _, err := workspace.Load(c.workspace); err != nil {
			return err
		}
		return fmt.Errorf("build --local: %s names no workspace", c.workspace)
	}
	d := rebuild.Dir{Path: filepath.Dir(c.db)}
	err := d.Run(name, func() (res rebuild.Result, err error) {
		// A build cut short by shutdown (killed plugins and all) is
		// cancelled, not failed: Run doesn't stamp it.
		defer func() {
			switch {
			case err == nil:
			case errors.Is(ctx.Err(), context.Canceled):
				err = fmt.Errorf("%w (%v)", context.Canceled, err)
			case c.background && errors.Is(ctx.Err(), context.DeadlineExceeded):
				// Run backs off for TimeoutBackoff on this.
				err = fmt.Errorf("automatic rebuild stopped after %s; run `cartographer build --local --import` by hand, or set `auto_rebuild = false` (%w: %v)", strings.TrimSuffix(rebuild.Timeout.String(), "0s"), context.DeadlineExceeded, err)
			}
		}()
		if !c.background {
			return localBuildLocked(ctx, w, os.Stderr, c, s, d, name)
		}
		f, err := os.Create(d.LogPath(name)) // the lock makes this the only writer
		if err != nil {
			return res, err
		}
		defer f.Close()
		res, err = localBuildLocked(ctx, f, f, c, s, d, name)
		if err != nil {
			fmt.Fprintln(f, "cartographer:", err)
		}
		return res, err
	})
	if errors.Is(err, rebuild.ErrRunning) {
		since, _ := d.Running(name)
		return fmt.Errorf("build --local: %w: %s, running since %s (log: %s); wait for it, or remove its lock in %s", rebuild.ErrRunning, name, since.Local().Format("15:04:05"), d.LogPath(name), d.StateDir(name))
	}
	return err
}

// localBuildLocked does localBuild's work; the caller holds the lock. With
// --import it returns the revisions imported and the store's database (a
// build left in --out changes nothing queries see).
func localBuildLocked(ctx context.Context, w, log io.Writer, c *common, s *store.Store, d rebuild.Dir, name string) (rebuild.Result, error) {
	var res rebuild.Result
	if c.background && userconfig.Trust(filepath.Dir(workspacePath(c.workspace))) != userconfig.Trusted {
		// Checked again here: the file may have changed since the query.
		return res, fmt.Errorf("cartographer.toml changed since you registered it (or was never registered): review, then `cartographer workspace add %s`", c.workspace)
	}
	cfg, err := workspace.Load(c.workspace)
	if err != nil {
		return res, err
	}
	ucfg, err := userconfig.Load()
	if err != nil {
		return res, err
	}
	paths, refs, err := workspace.Local(ctx, cfg, ucfg.Checkouts)
	if err != nil {
		return res, err
	}
	for repo, rev := range c.refs { // an explicit --ref still wins
		refs[repo] = rev
	}
	o := workspace.BuildOptions{Out: c.out, Paths: paths, Refs: refs, Log: log}
	if o.Out == "" && c.doImport {
		// Scratch: the snapshot is only imported. Scratch left by a
		// rebuild that died goes first.
		scratch, err := d.Scratch(name)
		if err != nil {
			return res, err
		}
		defer os.RemoveAll(scratch)
		o.Out = filepath.Join(scratch, "snapshot")
	}
	m, err := buildImport(ctx, w, c, cfg, o, s)
	if err != nil {
		return res, err
	}
	if !c.doImport {
		return res, nil
	}
	if res.DB, err = filepath.Abs(c.db); err != nil {
		return res, err
	}
	res.Revisions = map[string]string{}
	for _, r := range m.Repos {
		res.Revisions[r.Name] = r.Revision
	}
	return res, nil
}

// workspacePath is the cartographer.toml that --workspace names (a file,
// or a directory holding one).
func workspacePath(p string) string {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return filepath.Join(p, workspace.FileName)
	}
	return p
}

// buildCmd is `build` without --local (after Load).
func buildCmd(ctx context.Context, w io.Writer, c *common, cfg *workspace.Config) error {
	_, err := buildImport(ctx, w, c, cfg, workspace.BuildOptions{Out: c.out, Refs: c.refs, Log: os.Stderr, Fetch: c.fetch}, nil)
	return err
}

// buildImport resolves the plugins, builds into o.Out (default the
// workspace's .cartographer/snapshots) and, with --import, imports the
// snapshot and prunes the store to store.DefaultKeep per workspace.
func buildImport(ctx context.Context, w io.Writer, c *common, cfg *workspace.Config, o workspace.BuildOptions, s *store.Store) (*bundle.Manifest, error) {
	var err error
	if o.PluginsDir = c.plugins; o.PluginsDir == "" {
		if o.PluginsDir, err = plugins.Extract(workspace.DefaultCacheDir()); err != nil {
			return nil, err
		}
	}
	if cfg.Schema != nil {
		if o.PluginsDigest, err = schemaPluginsDigest(cfg.Schema.Command, o.PluginsDir); err != nil {
			return nil, err
		}
	}
	if o.Out == "" {
		o.Out = filepath.Join(cfg.Dir(), ".cartographer", "snapshots", fmt.Sprintf("build-%d", os.Getpid()))
	}
	m, err := workspace.Build(ctx, cfg, o)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(w, "built %s in %s\n", m.ID, o.Out)
	if !c.doImport {
		return m, nil
	}
	if s == nil {
		if s, err = openStore(c); err != nil {
			return nil, err
		}
		defer s.Close()
	}
	if _, err := s.Import(ctx, o.Out); err != nil {
		return nil, err
	}
	if err := s.Prune(ctx, store.DefaultKeep); err != nil {
		return nil, err
	}
	fmt.Fprintln(w, "imported", m.ID)
	return m, nil
}
