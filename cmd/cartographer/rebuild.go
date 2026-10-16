package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

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

// rebuildEnabled: local rebuilds are on unless --no-rebuild,
// $CARTOGRAPHER_NO_REBUILD or `auto_rebuild = false` in the user config.
func rebuildEnabled(c *common) bool {
	if c.noRebuild {
		return false
	}
	cfg, err := userconfig.Load()
	return err == nil && cfg.AutoRebuildOn()
}

// spawnRebuild is the Session.Rebuild of a one-shot command: it starts
// `cartographer build --local --import` detached, with its output in the
// rebuild log, and returns at once; the child takes the lock itself, so two
// commands racing here build once. Nothing here can fail the query.
func spawnRebuild(c *common) func(ctx context.Context, ws, dir string) {
	return func(_ context.Context, ws, dir string) {
		d := rebuild.Dir{Path: filepath.Dir(c.db)}
		exe, err := selfExe()
		if err != nil {
			return
		}
		db, err := filepath.Abs(c.db)
		if err != nil {
			return
		}
		if err := os.MkdirAll(filepath.Dir(d.LogPath(ws)), 0o755); err != nil {
			return
		}
		log, err := os.Create(d.LogPath(ws))
		if err != nil {
			return
		}
		defer log.Close()
		args := []string{"build", "--local", "--import", "--workspace", dir, "--db", db}
		if c.plugins != "" {
			args = append(args, "--plugins", c.plugins)
		}
		cmd := exec.Command(exe, args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = log, log
		detach(cmd)
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(log, "cartographer: could not start the rebuild: %v\n", err)
			return
		}
		cmd.Process.Release()
	}
}

// backgroundRebuild is the Session.Rebuild of a long-lived process (mcp,
// serve): the build runs in a goroutine, bounded by rebuild.Timeout,
// importing into the open store; the session forgets its cached choice when
// a new snapshot lands.
func backgroundRebuild(c *common, sess *session.Session, s *store.Store) func(ctx context.Context, ws, dir string) {
	return func(_ context.Context, ws, dir string) {
		go func() {
			d := rebuild.Dir{Path: filepath.Dir(c.db)}
			ctx, cancel := context.WithTimeout(context.Background(), rebuild.Timeout)
			defer cancel()
			os.MkdirAll(filepath.Dir(d.LogPath(ws)), 0o755)
			log, err := os.Create(d.LogPath(ws))
			if err != nil {
				return
			}
			defer log.Close()
			cc := *c
			cc.local, cc.doImport, cc.workspace, cc.out = true, true, dir, ""
			cfg, err := workspace.Load(dir)
			if err == nil {
				err = buildCmd(ctx, log, &cc, cfg, s)
			}
			switch {
			case errors.Is(err, rebuild.ErrRunning):
			case err != nil:
				if sess.Log != nil {
					fmt.Fprintf(sess.Log, "cartographer: local rebuild of %s failed (log: %s): %v\n", ws, d.LogPath(ws), err)
				}
			default:
				sess.Forget()
			}
		}()
	}
}

// buildCmd builds the workspace (after Load) and imports the snapshot when
// asked. A --local build snapshots every checkout at its HEAD (no fetch, no
// token) under the workspace's rebuild lock, and stamps the outcome for
// status; with --import and no --out its snapshot directory is scratch in
// the data directory, removed after the import. s is an open store to
// import into (nil opens --db).
func buildCmd(ctx context.Context, w io.Writer, c *common, cfg *workspace.Config, s *store.Store) error {
	o := workspace.BuildOptions{Out: c.out, Refs: c.refs, Log: os.Stderr, Fetch: c.fetch}
	if c.local {
		o.Log = w
		ucfg, err := userconfig.Load()
		if err != nil {
			return err
		}
		paths, refs, err := workspace.Local(ctx, cfg, ucfg.Checkouts)
		if err != nil {
			return err
		}
		for name, rev := range c.refs { // an explicit --ref still wins
			refs[name] = rev
		}
		o.Paths, o.Refs = paths, refs
	}
	var err error
	if o.PluginsDir = c.plugins; o.PluginsDir == "" {
		if o.PluginsDir, err = plugins.Extract(workspace.DefaultCacheDir()); err != nil {
			return err
		}
	}
	if cfg.Schema != nil {
		if o.PluginsDigest, err = schemaPluginsDigest(cfg.Schema.Command, o.PluginsDir); err != nil {
			return err
		}
	}
	d := rebuild.Dir{Path: filepath.Dir(c.db)}
	scratch := ""
	if o.Out == "" {
		if c.local && c.doImport {
			if err := os.MkdirAll(filepath.Join(d.Path, "rebuild"), 0o755); err != nil {
				return err
			}
			if scratch, err = os.MkdirTemp(filepath.Join(d.Path, "rebuild"), cfg.Name+".build-*"); err != nil {
				return err
			}
			defer os.RemoveAll(scratch)
			o.Out = filepath.Join(scratch, "snapshot")
		} else {
			o.Out = filepath.Join(cfg.Dir(), ".cartographer", "snapshots", fmt.Sprintf("build-%d", os.Getpid()))
		}
	}
	build := func() error {
		m, err := workspace.Build(ctx, cfg, o)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "built %s in %s\n", m.ID, o.Out)
		if !c.doImport {
			return nil
		}
		if s == nil {
			if s, err = openStore(c); err != nil {
				return err
			}
			defer s.Close()
		}
		if _, err := s.Import(ctx, o.Out); err != nil {
			return err
		}
		if err := s.Prune(ctx, store.DefaultKeep); err != nil {
			return err
		}
		fmt.Fprintln(w, "imported", m.ID)
		return nil
	}
	if !c.local {
		return build()
	}
	err = d.Run(cfg.Name, build)
	if errors.Is(err, rebuild.ErrRunning) {
		since, _ := d.Running(cfg.Name)
		return fmt.Errorf("build --local: a rebuild of %s has been running since %s (log: %s); wait for it, or remove its lock in %s", cfg.Name, since.Local().Format("15:04:05"), d.LogPath(cfg.Name), filepath.Join(d.Path, "rebuild"))
	}
	return err
}
