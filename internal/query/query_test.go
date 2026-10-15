package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/output"
	"github.com/shermanhuman/cartographer/internal/store"
)

func newQ(t *testing.T) *Q {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Import(context.Background(), "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	q, err := New(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func render(t *testing.T) func(*output.Doc, error) string {
	return func(d *output.Doc, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return d.Render(0)
	}
}

func has(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestResolve(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	for ref, want := range map[string]string{
		"shop-app:Shop.Orders.total/1": "shop-app:Shop.Orders.total/1",
		"Shop.Orders.total/1":          "shop-app:Shop.Orders.total/1",
		"Orders.total":                 "shop-app:Shop.Orders.total/1",
		"Shop.Billing":                 "shop-app:Shop.Billing",
		"public.orders":                "public.orders",
		"customers":                    "public.customers",
		"checkout":                     "ann:checkout",
	} {
		e, err := q.Resolve(ctx, ref)
		if err != nil || e.ID != want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", ref, e, err, want)
		}
	}
	_, err := q.Resolve(ctx, "create")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) || len(amb.Candidates) < 2 {
		t.Errorf("create: want ambiguous, got %v", err)
	}
	_, err = q.Resolve(ctx, "invoise")
	var nf *ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestFindAndVocabularyOnMiss(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Find(ctx, "order total", 10, 0))
	has(t, out, "map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb", "FN shop-app:Shop.Orders.total/1  lib/shop/orders.ex:20")
	out = render(t)(q.Find(ctx, "ordr", 10, 0))
	has(t, out, `no match for "ordr"`, "map vocabulary near your words", "orders")
}

func TestCallersAndCallees(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Calls(ctx, "Shop.Orders.total/1", 2, true, 0, 0))
	has(t, out,
		"callers of FN shop-app:Shop.Orders.total/1",
		"d1 FN shop-app:Shop.Billing.charge/2  lib/shop/billing.ex:5  [precise]",
		"d2 FN shop-app:ShopWeb.OrderController.create/2",
		"d2 FN shop-worker:Worker.Nightly.run/0  lib/worker/nightly.ex:3  [heuristic]  via shop-app:Shop.Billing.charge/2  ⇄ cross-repo",
	)
	out = render(t)(q.Calls(ctx, "Shop.Orders.create/2", 1, false, 0, 0))
	has(t, out, "d1 FN shop-app:Shop.Orders.validate/1  lib/shop/orders.ex:30  [precise] ×2")
	out = render(t)(q.Calls(ctx, "Shop.Billing.charge/2", 1, false, 0, 0))
	has(t, out, "+2 unresolved/external call sites")
}

func TestImpactGroupsReposAndTests(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Impact(ctx, "Shop.Orders.total/1", 3))
	has(t, out, "4 callers in 2 repo(s) · 1 cross-repo · 1 tests", "shop-worker ⇄ cross-repo", "\ntests\n", "Shop.OrdersTest.test_total/1")
	// impact of a table starts from functions that mention it
	out = render(t)(q.Impact(ctx, "public.orders", 2))
	has(t, out, "starting from functions whose source mentions the table (candidate)", "ShopWeb.OrderController.create/2")
}

func TestPath(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Path(ctx, "Worker.Nightly.run/0", "Shop.Orders.total/1"))
	has(t, out, "2 hop(s), calls", "0 FN shop-worker:Worker.Nightly.run/0", "2 FN shop-app:Shop.Orders.total/1")
	out = render(t)(q.Path(ctx, "Shop.Orders.total/1", "Worker.Nightly.run/0"))
	has(t, out, "reverse direction")
}

func TestShowTableContextStatus(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Show(ctx, "Shop.Orders.create/2"))
	has(t, out, "arity 1..2", "callers (1):", "callees (1):", `TBL public.orders  lib/shop/orders.ex:12`, `DOC shop-app:docs/orders.md:3  "Orders"`, `ANN ann:checkout  "Checkout"  (needs_review)`)
	out = render(t)(q.Table(ctx, "orders"))
	has(t, out, "total_cents integer not null default 0", "FK public.orders.customer_id → public.customers.id")
	out = render(t)(q.Context(ctx, "Shop.Orders"))
	has(t, out, "MOD shop-app:Shop.Orders  lib/shop/orders.ex  3 fn", "TBL public.orders ×1")
	out = render(t)(q.Status(ctx))
	has(t, out, "5 modules · 7 functions · 9 call sites", "call sites resolved: 7 of 9", "annotations: 1 needs_review")
}

func TestChangedFilesAreMarked(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	q.Changed = func(repo, file string) bool { return repo == "shop-app" && file == "lib/shop/billing.ex" }
	q.Staleness = "shop-app 2 commits behind"
	out := render(t)(q.Calls(ctx, "Shop.Orders.total/1", 1, true, 0, 0))
	has(t, out, "· shop-app 2 commits behind", "lib/shop/billing.ex:5 ⚠")
}

func TestBudgetAndPaging(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	d, err := q.Calls(ctx, "Shop.Orders.total/1", 3, true, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	has(t, d.Render(0), "more: offset=1")
}

func TestTableFallsBackToNameContains(t *testing.T) {
	q, ctx := newQ(t), context.Background()
	out := render(t)(q.Table(ctx, "custom"))
	has(t, out, "TBL public.customers")
	_, err := q.Table(ctx, "o")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) || !strings.Contains(err.Error(), "TBL public.orders") {
		t.Errorf("want table candidates, got %v", err)
	}
}
