// Command cartographer builds, serves and queries multi-repository system maps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
	"github.com/shermanhuman/cartographer/internal/hook"
	"github.com/shermanhuman/cartographer/internal/mcpserver"
	"github.com/shermanhuman/cartographer/internal/output"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/render"
	"github.com/shermanhuman/cartographer/internal/server"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/snapshot"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
	"github.com/shermanhuman/cartographer/internal/workspace"
	"github.com/shermanhuman/cartographer/plugins"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `cartographer — multi-repository system map

Queries (token-budgeted, ranked; --format json prints the rendered lines as
{"header": [...], "body": [...], "footer": [...]} arrays, unbudgeted):
  status                      snapshot, repos, coverage, annotation review state
  find <words> [--limit N] [--offset N]
                              search functions, modules, tables, files, docs
  show <ref>                  explain one entity and its neighbours
  callers <ref> [--depth N] [--limit N] [--offset N]
                              who calls it (cross-repo flagged)
  callees <ref> [--depth N] [--limit N] [--offset N]
                              what it calls
  impact <ref> [--depth N] [--limit N] [--offset N]
                              blast radius grouped by repo, context and tests
  path <a> <b>                shortest call chain
  table <name>                columns, keys and code that mentions a table
  context <name>              a module cluster: modules, tables, docs

Sharing (GitHub Markdown, PDF, SVG):
  mermaid <ref> [--depth N] [--max N]  Mermaid call graph (≤40 nodes)
  embed <ref>                 issue-ready Markdown: summary, diagram, pinned links
  diff <base> <head>          PR impact comment comparing two snapshots
  export --pdf|--svg [--page|--snippet] -o FILE
         (--around <ref> [--depth N] | --context <name> | --table <name> | --annotation <id>)
                              print view through headless Chrome (optional)

UI:
  serve [--port 7878] [--open]  browse the map at http://127.0.0.1:7878 (loopback only)

Snapshots:
  pull [owner/name]           fetch the newest published snapshot (adds the source)
  workspace add <owner/name>  pull snapshots from this repository's releases
  workspace add <dir>         rebuild from this local workspace when checkouts move
  import <dir>                import a snapshot directory
  snapshots                   list imported snapshots
  mapped                      exit 0 if the current directory is a mapped repo

Building (in a workspace with cartographer.toml):
  init [dir]                  write a cartographer.toml template
  build [--out DIR] [--ref repo=sha]... [--import] [--local | --fetch | --fetch-only]
                              snapshot every repo and run its extractor plugins;
                              --local maps each checkout at its HEAD (no fetch, no token)
  publish <snapshot-dir>      pack and publish as a GitHub release, prune old ones
  review <annotation-id>      mark an annotation reviewed against the snapshot's code
  validate-facts <file>...    check extractor plugin output (facts v1)
  plugins-dir                 print the directory of the bundled extractor plugins

Agents:
  mcp                         stdio MCP server exposing the queries as map_* tools
  hook grep                   PreToolUse hook: one nudge per session in mapped repos with code

Other:
  version

Common flags: --db PATH (or $CARTOGRAPHER_DB), --snapshot ID, --no-pull,
--no-rebuild; on queries also --budget TOKENS and --format text|json.
--limit/--offset page find, callers, callees and impact.
"cartographer <command> --help" lists the flags a command takes.
Queries pull newer snapshots at most hourly and mark lines in files changed
since the snapshot with ⚠. When a checkout has commits past the snapshot and
its workspace is known locally (a sibling directory, or "workspace add <dir>"),
queries rebuild the map in the background ("build --local --import").
A <ref> is an id, a label like Shop.Orders.create/2, or a unique search term.
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, errNotMapped) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "cartographer:", err)
		os.Exit(1)
	}
}

type common struct {
	db                                          string
	snapshot                                    string
	budget                                      int
	limit                                       int
	offset                                      int
	depth                                       int
	format                                      string
	noPull, noRebuild                           bool
	workspace                                   string
	out                                         string
	plugins                                     string
	doImport                                    bool
	fetch, fetchOnly, local                     bool
	refs                                        refFlags
	max                                         int
	port                                        int
	open                                        bool
	pdf, svg, page, snippet                     bool
	around, context, table, annotation, outFile string
}

type refFlags map[string]string

func (r *refFlags) String() string {
	if r == nil || len(*r) == 0 {
		return ""
	}
	return fmt.Sprint(map[string]string(*r))
}
func (r *refFlags) Set(v string) error {
	name, rev, ok := strings.Cut(v, "=")
	if !ok || name == "" || rev == "" {
		return fmt.Errorf("--ref wants repo=revision, got %q", v)
	}
	if *r == nil {
		*r = refFlags{}
	}
	(*r)[name] = rev
	return nil
}

func defaultDB() string {
	if p := os.Getenv("CARTOGRAPHER_DB"); p != "" {
		return p
	}
	return filepath.Join(session.DataDir(), "carto.db")
}

func flags(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&c.db, "db", defaultDB(), "database path")
	fs.StringVar(&c.snapshot, "snapshot", "", "snapshot id (default newest)")
	fs.IntVar(&c.budget, "budget", output.DefaultBudget, "output budget in tokens")
	fs.IntVar(&c.limit, "limit", 0, "max results")
	fs.IntVar(&c.offset, "offset", 0, "skip results")
	fs.IntVar(&c.depth, "depth", 0, "traversal depth")
	fs.StringVar(&c.format, "format", "text", "text or json")
	fs.BoolVar(&c.noPull, "no-pull", os.Getenv("CARTOGRAPHER_NO_PULL") != "", "don't check for newer snapshots")
	fs.BoolVar(&c.noRebuild, "no-rebuild", os.Getenv("CARTOGRAPHER_NO_REBUILD") != "", "don't rebuild the map from local checkouts that moved past the snapshot")
	fs.StringVar(&c.workspace, "workspace", ".", "workspace directory (build/publish)")
	fs.StringVar(&c.out, "out", "", "snapshot output directory (build)")
	fs.StringVar(&c.plugins, "plugins", os.Getenv("CARTOGRAPHER_PLUGINS"), "extractor plugins directory ({plugins})")
	fs.BoolVar(&c.doImport, "import", false, "import the built snapshot into the local store")
	fs.BoolVar(&c.fetch, "fetch", false, "clone or fetch repos before building (CI)")
	fs.BoolVar(&c.fetchOnly, "fetch-only", false, "clone or fetch repos, then stop: no plugin or prepare runs (CI: the only step with the token)")
	fs.BoolVar(&c.local, "local", false, "snapshot each checkout at its HEAD, located like freshness (siblings, [checkouts]); no fetch, no token")
	fs.Var(&c.refs, "ref", "repo=revision to snapshot instead of the configured ref (repeatable)")
	fs.IntVar(&c.max, "max", 0, "max diagram nodes (mermaid; default 40)")
	fs.IntVar(&c.port, "port", server.DefaultPort, "serve: port on 127.0.0.1")
	fs.BoolVar(&c.open, "open", false, "serve: open the browser")
	fs.BoolVar(&c.pdf, "pdf", false, "export: write a PDF")
	fs.BoolVar(&c.svg, "svg", false, "export: write the graph as SVG")
	fs.BoolVar(&c.page, "page", false, "export: whole page (default)")
	fs.BoolVar(&c.snippet, "snippet", false, "export: only the graph and details panel")
	fs.StringVar(&c.around, "around", "", "export: entity to center on")
	fs.StringVar(&c.context, "context", "", "export: context to show")
	fs.StringVar(&c.table, "table", "", "export: table to show")
	fs.StringVar(&c.annotation, "annotation", "", "export: annotation (journey, investigation, note) to show")
	fs.StringVar(&c.outFile, "o", "", "export: output file")
	return fs
}

// parse accepts flags before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos, nil
}

// commandFirst moves common flags given before the command
// (`cartographer --db X find y`) to after it.
func commandFirst(args []string) ([]string, error) {
	if !strings.HasPrefix(args[0], "-") {
		return args, nil
	}
	fs := flags("cartographer", &common{})
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%v; put the command first: cartographer <command> [flags]", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return nil, errors.New("no command; put the command first: cartographer <command> [flags]")
	}
	leading := args[:len(args)-len(rest)]
	return append(append([]string{rest[0]}, leading...), rest[1:]...), nil
}

func openStore(c *common) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(c.db), 0o755); err != nil {
		return nil, err
	}
	return store.Open(c.db)
}

func run(ctx context.Context, args []string, w io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(w, usage)
		return nil
	}
	args, err := commandFirst(args)
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	if _, ok := commandFlags[cmd]; !ok {
		return fmt.Errorf("unknown command %q (see `cartographer help`)", cmd)
	}
	var c common
	fs := flags(cmd, &c)
	fs.SetOutput(io.Discard)
	pos, err := parse(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		commandHelp(w, fs, cmd)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w (see `cartographer %s --help`)", cmd, err, cmd)
	}
	if err := checkFormat(cmd, c.format); err != nil {
		return err
	}
	need := func(n int, what string) error {
		if len(pos) < n {
			return fmt.Errorf("%s: need %s", cmd, what)
		}
		return nil
	}

	switch cmd {
	case "version":
		fmt.Fprintln(w, "cartographer", version)
		return nil
	case "validate-facts":
		if err := need(1, "a facts file"); err != nil {
			return err
		}
		return validateFacts(w, pos)
	case "import":
		if err := need(1, "a snapshot directory"); err != nil {
			return err
		}
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		id, err := s.Import(ctx, pos[0])
		if err != nil {
			return err
		}
		if err := s.Prune(ctx, store.DefaultKeep); err != nil {
			return err
		}
		fmt.Fprintln(w, "imported", id)
		return nil
	case "init":
		dir := "."
		if len(pos) > 0 {
			dir = pos[0]
		}
		target := filepath.Join(dir, workspace.FileName)
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("%s already exists", target)
		}
		if err := os.MkdirAll(filepath.Join(dir, "annotations"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(workspace.Template), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s; edit repos and plugins, then run `cartographer build --local --import`\n", target)
		return nil
	case "build":
		cfg, err := workspace.Load(c.workspace)
		if err != nil {
			return err
		}
		if c.local && (c.fetch || c.fetchOnly) {
			return errors.New("build: --local builds what is checked out; drop --fetch/--fetch-only")
		}
		if c.fetchOnly {
			if err := workspace.Fetch(ctx, cfg, c.refs, os.Stderr); err != nil {
				return err
			}
			fmt.Fprintf(w, "fetched %d repos\n", len(cfg.Repos))
			return nil
		}
		return buildCmd(ctx, w, &c, cfg, nil)
	case "publish":
		if err := need(1, "a snapshot directory"); err != nil {
			return err
		}
		cfg, err := workspace.Load(c.workspace)
		if err != nil {
			return err
		}
		if cfg.Publish == "" {
			return errors.New("publish: set `publish = \"owner/name\"` in cartographer.toml")
		}
		b, err := bundle.Read(pos[0])
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := bundle.Pack(pos[0], &buf); err != nil {
			return err
		}
		gh := snapshot.NewGitHub()
		if _, err := gh.Publish(ctx, cfg.Publish, b.Manifest.ID, releaseNotes(b), buf.Bytes()); err != nil {
			return err
		}
		n, err := gh.Prune(ctx, cfg.Publish, cfg.Keep)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "published %s to %s (%d bytes); pruned %d old snapshot(s)\n", b.Manifest.ID, cfg.Publish, buf.Len(), n)
		return nil
	case "review":
		if err := need(1, "an annotation id"); err != nil {
			return err
		}
		cfg, err := workspace.Load(c.workspace)
		if err != nil {
			return err
		}
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		sid := c.snapshot
		if sid == "" {
			if sid, err = s.Latest(ctx); err != nil {
				return err
			}
		}
		n, err := workspace.Review(ctx, cfg, s.DB, sid, pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "reviewed %s against %s: %d citations stamped; commit the annotation file\n", pos[0], sid, n)
		return nil
	case "plugins-dir":
		dir, err := plugins.Extract(workspace.DefaultCacheDir())
		if err != nil {
			return err
		}
		fmt.Fprintln(w, dir)
		return nil
	case "workspace":
		if len(pos) < 2 || pos[0] != "add" {
			return errors.New("usage: cartographer workspace add <owner/name | dir>")
		}
		// A directory holding cartographer.toml is a local workspace to
		// rebuild from; anything else is a GitHub source to pull from.
		if fi, err := os.Stat(filepath.Join(pos[1], workspace.FileName)); err == nil && !fi.IsDir() {
			dir, err := filepath.Abs(pos[1])
			if err != nil {
				return err
			}
			if err := userconfig.AddLocalWorkspace(dir); err != nil {
				return err
			}
			fmt.Fprintf(w, "added local workspace %s (%s) to %s\n", dir, workspace.Name(dir), userconfig.Path())
			return nil
		}
		if err := userconfig.AddWorkspace(pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(w, "added %s to %s\n", pos[1], userconfig.Path())
		return nil
	case "pull":
		if len(pos) > 0 {
			if err := userconfig.AddWorkspace(pos[0]); err != nil {
				return err
			}
		}
		cfg, err := userconfig.Load()
		if err != nil {
			return err
		}
		if len(cfg.Workspaces) == 0 {
			return errors.New("no workspaces configured: cartographer pull <owner/name>")
		}
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		sess := &session.Session{Store: s, DataDir: filepath.Dir(c.db)}
		var failed error
		for _, src := range cfg.Workspaces {
			res, err := sess.Puller().Pull(ctx, src)
			if err != nil {
				fmt.Fprintf(w, "%s: %v\n", src, err)
				failed = errors.New("some pulls failed")
				continue
			}
			state := "up to date"
			if res.Imported {
				state = "imported"
			}
			fmt.Fprintf(w, "%s: %s %s\n", src, res.Snapshot, state)
		}
		return failed
	case "mcp":
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		// stdout carries the MCP protocol; warnings go to stderr.
		sess := newSession(&c, s, os.Stderr)
		sess.Background = true // long-lived: never hold a tool call for a pull
		if sess.Rebuild != nil {
			sess.Rebuild = backgroundRebuild(&c, sess, s)
		}
		return mcpserver.Serve(ctx, mcpserver.Options{Version: version, Prepare: func(ctx context.Context) (*query.Q, error) {
			q, _, err := sess.Query(ctx)
			return q, err
		}, Extra: mcpserver.EmbedTool})
	case "hook":
		// Fail open: a hook never errors or blocks the agent.
		if len(pos) == 0 || pos[0] != "grep" {
			return nil
		}
		s, err := openStore(&c)
		if err != nil {
			return nil
		}
		defer s.Close()
		hook.Grep(ctx, os.Stdin, w, filepath.Dir(c.db), func(ctx context.Context, cwd string) bool {
			sess := newSession(&c, s, io.Discard)
			sess.Cwd, sess.AutoPull = cwd, false
			return sess.HasCode(ctx)
		})
		return nil
	case "mapped":
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		if !newSession(&c, s, io.Discard).Mapped(ctx) {
			return errNotMapped
		}
		fmt.Fprintln(w, "mapped")
		return nil
	case "serve":
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		return serve(ctx, w, s, &c)
	case "diff":
		if err := need(2, "base and head snapshot ids"); err != nil {
			return err
		}
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		out, err := render.Diff(ctx, s, pos[0], pos[1])
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, out)
		return err
	case "snapshots":
		s, err := openStore(&c)
		if err != nil {
			return err
		}
		defer s.Close()
		list, err := s.Snapshots(ctx)
		if err != nil {
			return err
		}
		for _, si := range list {
			fmt.Fprintf(w, "%s  %s  %s\n", si.ID, si.Workspace, si.CreatedAt)
		}
		return nil
	}

	s, err := openStore(&c)
	if err != nil {
		return err
	}
	defer s.Close()
	sess := newSession(&c, s, os.Stderr)
	q, rep, err := sess.Query(ctx)
	if errors.Is(err, store.ErrNoSnapshot) {
		return errors.New("no snapshot yet: run `cartographer pull <owner/name>` or `cartographer import <dir>`")
	}
	if err != nil {
		return err
	}
	switch cmd {
	case "mermaid", "embed":
		if err := need(1, "a ref"); err != nil {
			return err
		}
		var out string
		if cmd == "mermaid" {
			out, err = render.Mermaid(ctx, q, pos[0], c.depth, c.max)
		} else {
			out, err = render.Embed(ctx, q, pos[0])
		}
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, out)
		return err
	case "export":
		return exportCmd(ctx, w, s, q, &c)
	}
	d, err := Dispatch(ctx, q, cmd, pos, Opts{Depth: c.depth, Limit: c.limit, Offset: c.offset})
	if err != nil {
		return err
	}
	if cmd == "status" {
		for _, l := range rep.Lines() {
			d.L("%s", l)
		}
	}
	return write(w, d, c.format, c.budget)
}

var errNotMapped = errors.New("not in a mapped repository")

// newSession builds a session for cwd. A one-shot command rebuilds through
// a detached child (spawnRebuild); long-lived ones replace that with
// backgroundRebuild.
func newSession(c *common, s *store.Store, log io.Writer) *session.Session {
	cwd, _ := os.Getwd()
	sess := &session.Session{Store: s, DataDir: filepath.Dir(c.db), Cwd: cwd, Snapshot: c.snapshot, AutoPull: !c.noPull, Log: log}
	if rebuildEnabled(c) {
		sess.Rebuild = spawnRebuild(c)
	}
	return sess
}

func releaseNotes(b *bundle.Bundle) string {
	var lines []string
	lines = append(lines, "cartographer snapshot of workspace `"+b.Manifest.Workspace+"`.", "", "| repo | revision |", "|---|---|")
	for _, r := range b.Manifest.Repos {
		lines = append(lines, fmt.Sprintf("| %s | %s |", r.Name, r.Revision))
	}
	return strings.Join(lines, "\n")
}

// Opts are the per-query options shared by the CLI and MCP.
type Opts struct{ Depth, Limit, Offset int }

// Dispatch runs one named query.
func Dispatch(ctx context.Context, q *query.Q, cmd string, pos []string, o Opts) (*output.Doc, error) {
	need := func(n int, what string) error {
		if len(pos) < n {
			return fmt.Errorf("%s: need %s", cmd, what)
		}
		return nil
	}
	switch cmd {
	case "status":
		return q.Status(ctx)
	case "find":
		if err := need(1, "search words"); err != nil {
			return nil, err
		}
		return q.Find(ctx, strings.Join(pos, " "), o.Limit, o.Offset)
	case "show", "entity":
		if err := need(1, "a ref"); err != nil {
			return nil, err
		}
		return q.Show(ctx, pos[0])
	case "callers", "callees":
		if err := need(1, "a ref"); err != nil {
			return nil, err
		}
		return q.Calls(ctx, pos[0], o.Depth, cmd == "callers", o.Limit, o.Offset)
	case "impact":
		if err := need(1, "a ref"); err != nil {
			return nil, err
		}
		return q.Impact(ctx, pos[0], o.Depth, o.Limit, o.Offset)
	case "path":
		if err := need(2, "two refs"); err != nil {
			return nil, err
		}
		return q.Path(ctx, pos[0], pos[1])
	case "table":
		if err := need(1, "a table name"); err != nil {
			return nil, err
		}
		return q.Table(ctx, pos[0])
	case "context":
		if err := need(1, "a context or module name"); err != nil {
			return nil, err
		}
		return q.Context(ctx, pos[0])
	}
	return nil, fmt.Errorf("unknown command %q (see `cartographer help`)", cmd)
}

func write(w io.Writer, d *output.Doc, format string, budget int) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"header": d.Header, "body": d.Body, "footer": d.Footer})
	}
	_, err := io.WriteString(w, d.Render(budget))
	return err
}

func validateFacts(w io.Writer, paths []string) error {
	bad := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		d, err := facts.Decode(f)
		f.Close()
		if err == nil {
			err = d.Validate()
		}
		if err != nil {
			bad++
			fmt.Fprintf(w, "FAIL %s\n%v\n", p, err)
			continue
		}
		fmt.Fprintf(w, "ok   %s  (%d files, %d modules, %d functions, %d calls, %d tables)\n",
			p, len(d.Files), len(d.Modules), len(d.Functions), len(d.Calls), len(d.Tables))
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d documents invalid", bad, len(paths))
	}
	return nil
}
