// Package mcpserver exposes the query layer as MCP tools over stdio.
package mcpserver

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shermanhuman/cartographer/internal/output"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/store"
)

//go:embed instructions.md
var Instructions string

// Options configures the server.
type Options struct {
	Version string
	// Prepare returns a query context for each call, so a newly pulled
	// snapshot is picked up without restarting the server.
	Prepare func(ctx context.Context) (*query.Q, error)
	// Extra registers additional tools (e.g. map_embed from the render package).
	Extra func(s *mcp.Server, prepare func(ctx context.Context) (*query.Q, error))
}

// StoreOptions builds Options that query the newest snapshot in s.
func StoreOptions(version string, s *store.Store) Options {
	return Options{Version: version, Prepare: func(ctx context.Context) (*query.Q, error) {
		return query.New(ctx, s, "")
	}}
}

// Common input fields.
type Ref struct {
	Ref    string `json:"ref" jsonschema:"id (repo:Mod.fun/2), label (Mod.fun/2, Mod.fun, public.users) or search words"`
	Budget int    `json:"budget,omitempty" jsonschema:"output budget in tokens, default 2000"`
}

type Traverse struct {
	Ref    string `json:"ref" jsonschema:"id, label or search words"`
	Depth  int    `json:"depth,omitempty" jsonschema:"levels to follow (callers/callees default 1, impact default 3)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max results"`
	Offset int    `json:"offset,omitempty" jsonschema:"skip results (paging)"`
	Budget int    `json:"budget,omitempty" jsonschema:"output budget in tokens, default 2000"`
}

type FindIn struct {
	Terms  string `json:"terms" jsonschema:"words to search: identifiers, module or table names"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max results, default 25"`
	Offset int    `json:"offset,omitempty" jsonschema:"skip results (paging)"`
	Budget int    `json:"budget,omitempty" jsonschema:"output budget in tokens, default 2000"`
}

type PathIn struct {
	From   string `json:"from" jsonschema:"start: id, label or search words"`
	To     string `json:"to" jsonschema:"end: id, label or search words"`
	Budget int    `json:"budget,omitempty" jsonschema:"output budget in tokens, default 2000"`
}

type StatusIn struct {
	Budget int `json:"budget,omitempty" jsonschema:"output budget in tokens, default 2000"`
}

// New builds the MCP server with the map_* tools.
func New(o Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "cartographer", Version: o.Version}, &mcp.ServerOptions{Instructions: Instructions})
	run := func(ctx context.Context, budget int, f func(*query.Q) (*output.Doc, error)) (*mcp.CallToolResult, any, error) {
		q, err := o.Prepare(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNoSnapshot) {
				return text("no map snapshot is available yet; run `cartographer pull` (or build one) and retry", true), nil, nil
			}
			return nil, nil, err
		}
		d, err := f(q)
		if err != nil {
			var amb *query.ErrAmbiguous
			var nf *query.ErrNotFound
			if errors.As(err, &amb) || errors.As(err, &nf) {
				return text(err.Error(), true), nil, nil // agent-correctable: show candidates/vocabulary
			}
			return text(err.Error(), true), nil, nil
		}
		return text(d.Render(budget), false), nil, nil
	}

	mcp.AddTool(s, &mcp.Tool{Name: "map_status", Description: "Map freshness and coverage: snapshot id, repo revisions, commits behind, counts, annotation review state."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in StatusIn) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Status(ctx) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_find", Description: "Find where something is defined: search functions, modules, tables, files, docs by words. On a miss returns the map's vocabulary to retry with."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in FindIn) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Find(ctx, in.Terms, in.Limit, in.Offset) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_entity", Description: "Explain one function, module, table, context or annotation: location, top callers/callees, tables, docs, review status."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Ref) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Show(ctx, in.Ref) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_callers", Description: "Who calls a function (or any function of a module, file, context, or code mentioning a table); cross-repo callers flagged."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Traverse) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) {
				return q.Calls(ctx, in.Ref, in.Depth, true, in.Limit, in.Offset)
			})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_callees", Description: "What a function calls (resolved calls; unresolved/external sites counted)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Traverse) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) {
				return q.Calls(ctx, in.Ref, in.Depth, false, in.Limit, in.Offset)
			})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_impact", Description: "Blast radius / what breaks if this changes: transitive callers grouped by repo, context and tests."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Traverse) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Impact(ctx, in.Ref, in.Depth) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_path", Description: "Shortest call path between two functions (how A reaches B)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in PathIn) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Path(ctx, in.From, in.To) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_table", Description: "Database table schema: columns, constraints, foreign keys, and code that mentions the table (candidate)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Ref) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Table(ctx, in.Ref) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "map_context", Description: "A subsystem (module namespace cluster): its modules, the tables its code mentions, and docs."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in Ref) (*mcp.CallToolResult, any, error) {
			return run(ctx, in.Budget, func(q *query.Q) (*output.Doc, error) { return q.Context(ctx, in.Ref) })
		})
	if o.Extra != nil {
		o.Extra(s, o.Prepare)
	}
	return s
}

func text(s string, isErr bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimRight(s, "\n")}}, IsError: isErr}
}

// Serve runs the server on stdio until the client disconnects.
func Serve(ctx context.Context, o Options) error {
	return New(o).Run(ctx, &mcp.StdioTransport{})
}
