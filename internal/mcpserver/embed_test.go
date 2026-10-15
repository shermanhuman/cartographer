package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shermanhuman/cartographer/internal/store"
)

func TestEmbedTool(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Import(ctx, "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	o := StoreOptions("test", s)
	o.Extra = EmbedTool
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(o).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	md, isErr := call(t, cs, "map_embed", map[string]any{"ref": "Shop.Billing.charge/2"})
	if isErr || !strings.Contains(md, "```mermaid") || !strings.Contains(md, "/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/billing.ex#L5") {
		t.Errorf("markdown (%v):\n%s", isErr, md)
	}
	mm, isErr := call(t, cs, "map_embed", map[string]any{"ref": "Shop.Orders.total/1", "kind": "mermaid", "max": 2})
	if isErr || !strings.HasPrefix(mm, "```mermaid\ngraph LR") || !strings.Contains(mm, "+1 more") {
		t.Errorf("mermaid (%v):\n%s", isErr, mm)
	}
	if out, isErr := call(t, cs, "map_embed", map[string]any{"ref": "x", "kind": "svg"}); !isErr {
		t.Errorf("bad kind accepted: %s", out)
	}
}
