package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/render"
	"github.com/shermanhuman/cartographer/internal/store"
)

// EmbedIn is the map_embed input.
type EmbedIn struct {
	Ref   string `json:"ref" jsonschema:"id, label (Mod.fun/2, public.users) or search words"`
	Kind  string `json:"kind,omitempty" jsonschema:"markdown (default: issue-ready summary + diagram + pinned source links) or mermaid (diagram only)"`
	Depth int    `json:"depth,omitempty" jsonschema:"caller/callee levels in the diagram, default 1"`
	Max   int    `json:"max,omitempty" jsonschema:"max diagram nodes, default 40"`
}

// EmbedTool registers map_embed; pass it as Options.Extra.
func EmbedTool(s *mcp.Server, prepare func(ctx context.Context) (*query.Q, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: "map_embed", Description: "Ready-to-post GitHub Markdown for an issue or PR comment: summary table, Mermaid call graph (≤40 nodes) and source links pinned to the snapshot revisions. kind=mermaid returns only the diagram."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in EmbedIn) (*mcp.CallToolResult, any, error) {
			q, err := prepare(ctx)
			if err != nil {
				if errors.Is(err, store.ErrNoSnapshot) {
					return text("no map snapshot is available yet; run `cartographer pull` (or build one) and retry", true), nil, nil
				}
				return nil, nil, err
			}
			var out string
			switch in.Kind {
			case "", "markdown":
				out, err = render.Embed(ctx, q, in.Ref)
			case "mermaid":
				out, err = render.Mermaid(ctx, q, in.Ref, in.Depth, in.Max)
				if err == nil {
					out = "```mermaid\n" + out + "```\n"
				}
			default:
				return text(`kind must be "markdown" or "mermaid"`, true), nil, nil
			}
			if err != nil {
				return text(err.Error(), true), nil, nil
			}
			return text(out, false), nil, nil
		})
}
