package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func cli(t *testing.T, db string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(context.Background(), append(args, "--db", db), &out); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestCLIEndToEnd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	fixture := "../../testdata/workspace-mini/snapshot"
	if out := cli(t, db, "import", fixture); !strings.Contains(out, "imported snapshot-20261015T120000Z") {
		t.Fatal(out)
	}
	for args, want := range map[string]string{
		"callers Shop.Orders.total/1 --depth 2": "⇄ cross-repo",
		"impact public.orders":                  "impact of TBL public.orders",
		"find billing":                          "MOD shop-app:Shop.Billing",
		"table orders --format json":            `"TBL public.orders"`,
		"status":                                "7 functions",
		"mermaid Shop.Orders.total/1 --max 2":   "+1 more → cartographer impact shop-app:Shop.Orders.total/1",
		"embed public.orders":                   "```mermaid\ngraph LR",
		"diff snapshot-20261015T120000Z snapshot-20261015T120000Z": "## cartographer impact",
	} {
		if out := cli(t, db, strings.Fields(args)...); !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", args, want, out)
		}
	}
	if out := cli(t, db, "validate-facts", fixture+"/facts/shop-app.json"); !strings.HasPrefix(out, "ok") {
		t.Error(out)
	}
}

func TestNoSnapshotMessage(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"status", "--db", filepath.Join(t.TempDir(), "x.db")}, &out)
	if err == nil || !strings.Contains(err.Error(), "cartographer pull") {
		t.Fatalf("got %v", err)
	}
}

func TestExportFlagsValidated(t *testing.T) {
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "import", "../../testdata/workspace-mini/snapshot")
	for _, args := range [][]string{
		{"export", "--around", "Shop.Orders.total/1", "-o", "x.pdf"},
		{"export", "--pdf", "--svg", "--around", "x", "-o", "x"},
		{"export", "--pdf", "--around", "Shop.Orders.total/1"},
		{"export", "--pdf", "-o", filepath.Join(t.TempDir(), "x.pdf")},
		{"serve", "--port", "-1"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), append(args, "--db", db), &out); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}
