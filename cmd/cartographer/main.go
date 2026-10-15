// Command cartographer builds, serves and queries multi-repository system maps.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shermanhuman/cartographer/internal/facts"
	"github.com/shermanhuman/cartographer/internal/output"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `cartographer — multi-repository system map

Queries (token-budgeted, ranked; --format json for structured output):
  status                      snapshot, repos, coverage, annotation review state
  find <words>                search functions, modules, tables, files, docs
  show <ref>                  explain one entity and its neighbours
  callers <ref> [--depth N]   who calls it (cross-repo flagged)
  callees <ref> [--depth N]   what it calls
  impact <ref> [--depth N]    blast radius grouped by repo, context and tests
  path <a> <b>                shortest call chain
  table <name>                columns, keys and code that mentions a table
  context <name>              a module cluster: modules, tables, docs

Snapshots:
  import <dir>                import a snapshot directory
  snapshots                   list imported snapshots
  validate-facts <file>...    check extractor plugin output (facts v1)

Other:
  version

Common flags: --db PATH (or $CARTOGRAPHER_DB), --snapshot ID, --budget TOKENS,
--limit N, --offset N, --format text|json.
A <ref> is an id, a label like Shop.Orders.create/2, or a unique search term.
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cartographer:", err)
		os.Exit(1)
	}
}

type common struct {
	db       string
	snapshot string
	budget   int
	limit    int
	offset   int
	depth    int
	format   string
}

func defaultDB() string {
	if p := os.Getenv("CARTOGRAPHER_DB"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "cartographer", "carto.db")
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
	cmd, args := args[0], args[1:]
	var c common
	fs := flags(cmd, &c)
	pos, err := parse(fs, args)
	if err != nil {
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
	q, err := query.New(ctx, s, c.snapshot)
	if errors.Is(err, store.ErrNoSnapshot) {
		return errors.New("no snapshot yet: run `cartographer pull` or `cartographer import <dir>`")
	}
	if err != nil {
		return err
	}
	d, err := Dispatch(ctx, q, cmd, pos, Opts{Depth: c.depth, Limit: c.limit, Offset: c.offset})
	if err != nil {
		return err
	}
	return write(w, d, c.format, c.budget)
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
		return q.Impact(ctx, pos[0], o.Depth)
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
