package render

import (
	"context"
	"encoding/json"
	"flag"
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

// headSnapshot copies the fixture and edits its facts: Billing.charge/2 no
// longer calls Orders.total and its body changed, total/1 became total/2, and
// a new Orders.cancel/1 mentions the orders table.
func headSnapshot(t *testing.T) string {
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
