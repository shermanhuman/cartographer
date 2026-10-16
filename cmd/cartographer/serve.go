package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/shermanhuman/cartographer/internal/export"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/server"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
)

func serve(ctx context.Context, w io.Writer, s *store.Store, c *common) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := server.Listen("127.0.0.1", c.port)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s/", ln.Addr())
	if c.snapshot != "" {
		url += "#snapshot=" + c.snapshot
	}
	fmt.Fprintf(w, "cartographer map at %s (Ctrl-C to stop)\n", url)
	if c.open {
		openBrowser(url)
	}
	sess := newSession(c, s, os.Stderr)
	sess.Background = true
	if sess.Rebuild != nil {
		// Ctrl-C cancels ctx: a running rebuild stops and cleans up first.
		start, wait := backgroundRebuild(ctx, c, sess, s)
		sess.Rebuild = start
		defer func() { stop(); wait() }()
	}
	if sess.AutoPull {
		sess.PullDue(ctx)
	}
	return server.Serve(ctx, ln, server.New(server.Options{Store: s, Version: version, Status: serveStatus(sess), Default: sess.Choose}))
}

// serveStatus reports freshness for /api/status, which the UI loads on every
// page. Each call also starts a due pull in the background, so a long-running
// serve picks up newly published snapshots on a later page load.
func serveStatus(sess *session.Session) func(ctx context.Context, snapshot string) (any, error) {
	return func(ctx context.Context, snapshot string) (any, error) {
		if sess.AutoPull {
			sess.PullDue(ctx)
		}
		rep, err := sess.Fresh(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"summary": rep.Summary(), "stale": rep.Summary() != "", "repos": rep.Lines()}, nil
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

func exportCmd(ctx context.Context, w io.Writer, s *store.Store, q *query.Q, c *common) error {
	if c.pdf == c.svg {
		return errors.New("export: choose one of --pdf or --svg")
	}
	if c.outFile == "" {
		return errors.New("export: need -o FILE")
	}
	if c.page && c.snippet {
		return errors.New("export: choose one of --page or --snippet")
	}
	t := export.Target{Around: c.around, Context: c.context, Table: c.table, Annotation: c.annotation, Depth: c.depth}
	// Render the snapshot the target was resolved in (chosen from cwd when
	// --snapshot is not given), not the newest one.
	o := export.Options{Format: "pdf", Mode: "page", Snapshot: q.Snapshot}
	if c.svg {
		o.Format = "svg"
	}
	if c.snippet {
		o.Mode = "snippet"
	}
	frag, err := export.Fragment(ctx, q, t)
	if err != nil {
		return err
	}
	data, err := export.Run(ctx, server.New(server.Options{Store: s, Version: version}), frag, o)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.outFile, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (%d bytes)\n", c.outFile, len(data))
	return nil
}
