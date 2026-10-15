package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shermanhuman/cartographer/internal/store"
)

func session(t *testing.T, imported bool) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if imported {
		if _, err := s.Import(ctx, "../../testdata/workspace-mini/snapshot"); err != nil {
			t.Fatal(err)
		}
	}
	server := New(StoreOptions("test", s))
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestToolsListedWithShortDescriptions(t *testing.T) {
	cs := session(t, true)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"map_status": true, "map_find": true, "map_entity": true, "map_callers": true, "map_callees": true, "map_impact": true, "map_path": true, "map_table": true, "map_context": true}
	for _, tool := range res.Tools {
		delete(want, tool.Name)
		if len(tool.Description) > 300 {
			t.Errorf("%s description is %d chars", tool.Name, len(tool.Description))
		}
	}
	if len(want) > 0 {
		t.Errorf("missing tools: %v", want)
	}
	if init := cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "map_impact") || len(init.Instructions) > 1500 {
		t.Errorf("instructions missing or too long")
	}
}

func TestCallersCompactText(t *testing.T) {
	cs := session(t, true)
	out, isErr := call(t, cs, "map_callers", map[string]any{"ref": "Shop.Orders.total/1", "depth": 2})
	if isErr || !strings.Contains(out, "d1 FN shop-app:Shop.Billing.charge/2  lib/shop/billing.ex:5  [precise]") {
		t.Errorf("got (%v):\n%s", isErr, out)
	}
}

func TestBudgetTruncates(t *testing.T) {
	cs := session(t, true)
	out, _ := call(t, cs, "map_impact", map[string]any{"ref": "Shop.Orders.total/1", "budget": 60})
	if !strings.Contains(out, "more lines (raise budget") {
		t.Errorf("no truncation note:\n%s", out)
	}
}

func TestAgentCorrectableErrors(t *testing.T) {
	cs := session(t, true)
	out, isErr := call(t, cs, "map_entity", map[string]any{"ref": "create"})
	if !isErr || !strings.Contains(out, "matches") || !strings.Contains(out, "shop-app:Shop.Orders.create/2") {
		t.Errorf("ambiguous: (%v) %s", isErr, out)
	}
	out, _ = call(t, cs, "map_find", map[string]any{"terms": "ordr"})
	if !strings.Contains(out, "orders") {
		t.Errorf("vocabulary suggestion missing: %s", out)
	}
}

func TestNoSnapshot(t *testing.T) {
	cs := session(t, false)
	out, isErr := call(t, cs, "map_status", map[string]any{})
	if !isErr || !strings.Contains(out, "cartographer pull") {
		t.Errorf("got (%v) %s", isErr, out)
	}
}
