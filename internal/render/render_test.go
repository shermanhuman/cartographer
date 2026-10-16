package render

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "../../testdata/workspace-mini/snapshot"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Import(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	return s
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/render -update`)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden (run with -update to accept):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestMermaidGolden(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	q, err := query.New(ctx, s, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, ref  string
		depth, max int
	}{
		{"mermaid-total.md", "Shop.Orders.total/1", 0, 0},
		{"mermaid-charge-depth2.md", "Shop.Billing.charge/2", 2, 0},
		{"mermaid-capped.md", "Shop.Orders", 1, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Mermaid(ctx, q, c.ref, c.depth, c.max)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got, "graph LR\n") {
				t.Fatalf("not a graph LR diagram:\n%s", got)
			}
			golden(t, c.name, got)
		})
	}
}

func TestMermaidCapNote(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	q, _ := query.New(ctx, s, "")
	got, err := Mermaid(ctx, q, "Shop.Orders", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "more → cartographer impact shop-app:Shop.Orders") {
		t.Errorf("missing cap note:\n%s", got)
	}
	if n := strings.Count(got, `["`) - 1; n != 3 { // nodes minus the note
		t.Errorf("want 3 nodes, got %d:\n%s", n, got)
	}
}

func TestEmbedGolden(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	q, _ := query.New(ctx, s, "")
	for name, ref := range map[string]string{
		"embed-charge.md": "Shop.Billing.charge/2",
		"embed-table.md":  "public.orders",
	} {
		got, err := Embed(ctx, q, ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > MaxChars {
			t.Errorf("%s: %d chars > %d", name, len(got), MaxChars)
		}
		golden(t, name, got)
	}
	got, _ := Embed(ctx, q, "Shop.Billing.charge/2")
	for _, want := range []string{
		"https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/billing.ex#L5",
		"```mermaid\ngraph LR",
		"| Cross-repo callers | 1 |",
		"map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("embed missing %q:\n%s", want, got)
		}
	}
}

// fixtureCopy copies the fixture snapshot to a temp dir and returns an
// editor for its JSON files.
func fixtureCopy(t *testing.T) (string, func(rel string, f func(m map[string]any))) {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(fixture, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixture, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := func(rel string, f func(m map[string]any)) {
		p := filepath.Join(dir, rel)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, edit
}

// headSnapshot copies the fixture and edits its facts: Billing.charge/2 no
// longer calls Orders.total and its body changed, total/1 became total/2, and
// a new Orders.cancel/1 mentions the orders table.
func headSnapshot(t *testing.T) string {
	t.Helper()
	dir, edit := fixtureCopy(t)
	edit("manifest.json", func(m map[string]any) {
		m["id"] = "snapshot-20261016T120000Z"
		m["created_at"] = "2026-10-16T12:00:00Z"
		m["repos"].([]any)[0].(map[string]any)["revision"] = "cccccccccccccccccccccccccccccccccccccccc"
	})
	edit("facts/shop-app.json", func(m map[string]any) {
		m["revision"] = "cccccccccccccccccccccccccccccccccccccccc"
		var fns []any
		for _, f := range m["functions"].([]any) {
			fn := f.(map[string]any)
			switch fn["id"] {
			case "shop-app:Shop.Billing.charge/2":
				fn["fingerprint"] = "h3-new"
			case "shop-app:Shop.Orders.total/1":
				fn["id"] = "shop-app:Shop.Orders.total/2"
				fn["arity"], fn["min_arity"] = 2, 2
			}
			fns = append(fns, fn)
		}
		fns = append(fns, map[string]any{
			"id": "shop-app:Shop.Orders.cancel/1", "module": "shop-app:Shop.Orders", "name": "cancel",
			"arity": 1, "min_arity": 1, "kind": "def", "file": "lib/shop/orders.ex", "line": 40, "clauses": []any{40},
		})
		m["functions"] = fns
		var calls []any
		for _, c := range m["calls"].([]any) {
			call := c.(map[string]any)
			tgt, _ := call["target"].(map[string]any)
			if call["from"] == "shop-app:Shop.Billing.charge/2" && tgt["name"] == "total" {
				continue
			}
			calls = append(calls, call)
		}
		calls = append(calls, map[string]any{
			"from": "shop-app:Shop.Orders.cancel/1", "kind": "local", "file": "lib/shop/orders.ex", "line": 41,
			"target": map[string]any{"module": "Shop.Orders", "name": "validate", "arity": 1},
		})
		m["calls"] = calls
		m["table_refs"] = append(m["table_refs"].([]any), map[string]any{
			"table": "public.orders", "file": "lib/shop/orders.ex", "line": 42,
			"function": "shop-app:Shop.Orders.cancel/1", "text": "Repo.update(order) # orders",
		})
	})
	return dir
}

func TestDiffGolden(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	head, err := s.Import(ctx, headSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(ctx, s, "snapshot-20261015T120000Z", head)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "diff.md", got)
	for _, want := range []string{
		"## cartographer impact",
		"| signature | ",
		"/1 → /2",
		"| body | ",
		"| added | ",
		"still calls removed `Shop.Orders.total/1`",
		"Worker.Nightly.run/0",
		"⇄ cross-repo",
		"`public.orders` ← ",
		"linkStyle",
		"base <code>snapshot-20261015T120000Z</code>",
		"head <code>snapshot-20261016T120000Z</code> shop-app@ccccccc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q", want)
		}
	}
	if len(got) > MaxChars {
		t.Errorf("diff is %d chars", len(got))
	}

	same, err := Diff(ctx, s, head, head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(same, "No function, call or table-reference changes") {
		t.Errorf("identical snapshots:\n%s", same)
	}
	if _, err := Diff(ctx, s, "nope", head); err == nil {
		t.Error("unknown snapshot should fail")
	}
}

func TestCapChars(t *testing.T) {
	long := "```mermaid\n" + strings.Repeat("x\n", MaxChars)
	got := capChars(long)
	if len(got) > MaxChars {
		t.Errorf("capChars: %d", len(got))
	}
	if strings.Count(got, "```")%2 != 0 {
		t.Error("capChars left a fence open")
	}
}

// A function whose arity set changed and whose body also changed is listed
// once, as a signature change, and counted once.
func TestDiffSignatureChangeNotAlsoBody(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	// Base: the fixture with a fingerprint on total/1.
	baseDir, editBase := fixtureCopy(t)
	editBase("facts/shop-app.json", func(m map[string]any) {
		for _, f := range m["functions"].([]any) {
			if fn := f.(map[string]any); fn["id"] == "shop-app:Shop.Orders.total/1" {
				fn["fingerprint"] = "total-old-body"
			}
		}
	})
	base, err := s.Import(ctx, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	// Head: total/1 keeps its id with a new body, and total/2 is added.
	dir, edit := fixtureCopy(t)
	edit("manifest.json", func(m map[string]any) {
		m["id"] = "snapshot-20261016T120000Z"
		m["created_at"] = "2026-10-16T12:00:00Z"
	})
	edit("facts/shop-app.json", func(m map[string]any) {
		var fns []any
		for _, f := range m["functions"].([]any) {
			fn := f.(map[string]any)
			if fn["id"] == "shop-app:Shop.Orders.total/1" {
				fn["fingerprint"] = "total-new-body"
				two := map[string]any{}
				for k, v := range fn {
					two[k] = v
				}
				two["id"] = "shop-app:Shop.Orders.total/2"
				two["arity"], two["min_arity"] = 2, 2
				fns = append(fns, two)
			}
			fns = append(fns, fn)
		}
		m["functions"] = fns
	})
	head, err := s.Import(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(ctx, s, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "| signature | ") {
		t.Fatalf("want a signature row:\n%s", got)
	}
	if strings.Contains(got, "| body | ") {
		t.Errorf("total/1 listed as both signature- and body-changed:\n%s", got)
	}
	if !strings.Contains(got, "**1** changed") {
		t.Errorf("want 1 changed function:\n%s", got)
	}
}

func TestClampDiagram(t *testing.T) {
	for _, c := range []struct{ depth, max, wantDepth, wantMax int }{
		{0, 0, 1, DefaultMaxNodes},
		{-3, -1, 1, DefaultMaxNodes},
		{2, 10, 2, 10},
		{99, 100000, MaxDepth, MaxNodesLimit},
	} {
		d, m := ClampDiagram(c.depth, c.max)
		if d != c.wantDepth || m != c.wantMax {
			t.Errorf("ClampDiagram(%d, %d) = %d, %d; want %d, %d", c.depth, c.max, d, m, c.wantDepth, c.wantMax)
		}
	}
	s := openStore(t)
	ctx := context.Background()
	q, _ := query.New(ctx, s, "")
	if _, err := Mermaid(ctx, q, "Shop.Orders", 1000, 1000000); err != nil {
		t.Fatalf("huge depth/max should be clamped, not fail: %v", err)
	}
}

// A diagram whose labels push it past MaxChars drops its farthest nodes and
// says so, rather than producing Markdown GitHub rejects.
func TestMermaidFitsMaxChars(t *testing.T) {
	nb := &neighbourhood{Entity: &query.Entity{ID: "x:big"}}
	for i := 0; i < MaxNodesLimit; i++ {
		id := fmt.Sprintf("n%03d", i)
		nb.Nodes = append(nb.Nodes, mnode{ID: id, Label: strings.Repeat("L", 600), Repo: fmt.Sprintf("r%d", i%3)})
		if i > 0 {
			nb.Links = append(nb.Links, mlink{From: "n000", To: id})
		}
	}
	got := nb.mermaid("provenance")
	if len(got) > MaxChars {
		t.Fatalf("diagram is %d chars > %d", len(got), MaxChars)
	}
	if !strings.HasPrefix(got, "graph LR\n") || !strings.Contains(got, "more → cartographer impact x:big") {
		t.Errorf("want a trimmed diagram with a hidden-node note:\n%.300s", got)
	}
	if strings.Count(got, "subgraph") != strings.Count(got, "  end\n") {
		t.Error("unbalanced subgraph/end after trimming")
	}
}

// diffEdited imports the fixture as base (after editBase) and as a head
// snapshot (after editHead), and returns the rendered diff.
func diffEdited(t *testing.T, editBase, editHead func(m map[string]any)) string {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	baseDir, eb := fixtureCopy(t)
	eb("facts/shop-app.json", editBase)
	base, err := s.Import(ctx, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	headDir, eh := fixtureCopy(t)
	eh("manifest.json", func(m map[string]any) {
		m["id"] = "snapshot-20261016T120000Z"
		m["created_at"] = "2026-10-16T12:00:00Z"
	})
	eh("facts/shop-app.json", func(m map[string]any) {
		editBase(m)
		editHead(m)
	})
	head, err := s.Import(ctx, headDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(ctx, s, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxChars {
		t.Errorf("diff is %d chars", len(got))
	}
	return got
}

// A local call (target without a module) resolved through the caller's
// module in base; head removes the callee, so the surviving caller's call
// is now unresolved.
func TestDiffDanglingLocalCall(t *testing.T) {
	got := diffEdited(t, func(m map[string]any) {
		for _, c := range m["calls"].([]any) {
			call := c.(map[string]any)
			if tgt := call["target"].(map[string]any); tgt["name"] == "validate" {
				delete(tgt, "module")
			}
		}
	}, func(m map[string]any) {
		var fns []any
		for _, f := range m["functions"].([]any) {
			if f.(map[string]any)["id"] != "shop-app:Shop.Orders.validate/1" {
				fns = append(fns, f)
			}
		}
		m["functions"] = fns
	})
	if !strings.Contains(got, "| removed | `Shop.Orders.validate/1`") {
		t.Fatalf("want validate/1 removed:\n%s", got)
	}
	if !strings.Contains(got, "still calls removed `validate/1` at lib/shop/orders.ex:11 (now unresolved)") {
		t.Errorf("Shop.Orders.create/2 should be flagged as a dangling caller:\n%s", got)
	}
}

// Dropping a default argument keeps the function ID (same max arity) but
// removes a callable arity; a surviving call at the lost arity is flagged.
func TestDiffDanglingLostArity(t *testing.T) {
	got := diffEdited(t, func(map[string]any) {}, func(m map[string]any) {
		for _, f := range m["functions"].([]any) {
			if fn := f.(map[string]any); fn["id"] == "shop-app:Shop.Orders.create/2" {
				fn["min_arity"] = 2
			}
		}
	})
	if !strings.Contains(got, "/1, /2 → /2") {
		t.Fatalf("want create /1, /2 → /2 signature change:\n%s", got)
	}
	if !strings.Contains(got, "still calls removed `Shop.Orders.create/1` at lib/shop_web/order_controller.ex:9 (now unresolved)") {
		t.Errorf("ShopWeb.OrderController.create/2 should be flagged as a dangling caller:\n%s", got)
	}
}

// A caller that simply stops calling a function is not dangling.
func TestDiffRemovedCallNotDangling(t *testing.T) {
	got := diffEdited(t, func(map[string]any) {}, func(m map[string]any) {
		var calls []any
		for _, c := range m["calls"].([]any) {
			if c.(map[string]any)["from"] != "shop-app:Shop.OrdersTest.test_total/1" {
				calls = append(calls, c)
			}
		}
		m["calls"] = calls
	})
	if strings.Contains(got, "still calls") {
		t.Errorf("no caller should dangle:\n%s", got)
	}
}
