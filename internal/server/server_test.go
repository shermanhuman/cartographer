package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/store"
)

const fixture = "../../testdata/workspace-mini/snapshot"
const snap = "snapshot-20261015T120000Z"

func newServer(t *testing.T, imported bool) *httptest.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if imported {
		if _, err := s.Import(context.Background(), fixture); err != nil {
			t.Fatal(err)
		}
	}
	h := New(Options{
		Store: s, Version: "test",
		Now: func() time.Time { return time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC) },
		Status: func(_ context.Context, id string) (any, error) {
			return map[string]any{"summary": "shop-app 2 commits behind", "stale": true, "snapshot": id}, nil
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string, want int) (string, http.Header) {
	t.Helper()
	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d: %s", path, res.StatusCode, want, body)
	}
	return string(body), res.Header
}

func getJSON[T any](t *testing.T, srv *httptest.Server, path string) T {
	t.Helper()
	body, h := get(t, srv, path, 200)
	if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET %s: content type %q", path, ct)
	}
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("GET %s: %v\n%s", path, err, body)
	}
	return v
}

func list(v any) []any { l, _ := v.([]any); return l }
func asObj(v any) obj  { o, _ := v.(obj); return o }

func byID(entries []obj) map[string]obj {
	m := map[string]obj{}
	for _, e := range entries {
		m[e["id"].(string)] = e
	}
	return m
}

func TestCatalogShape(t *testing.T) {
	srv := newServer(t, true)
	cat := byID(getJSON[[]obj](t, srv, "/api/catalog"))
	for id, kind := range map[string]string{
		"shop-app:Shop.Orders":             "module",
		"shop-app:Shop.Orders.total/1":     "function",
		"public.orders":                    "table",
		"s:shop-app":                       "service",
		"s:database":                       "service",
		"ctx:shop-app:Shop.Orders":         "context",
		"ann:checkout":                     "annotation",
		"shop-worker:Worker.Nightly.run/0": "function",
	} {
		e, ok := cat[id]
		if !ok {
			t.Errorf("catalog lacks %s", id)
			continue
		}
		if e["kind"] != kind {
			t.Errorf("%s kind %v, want %s", id, e["kind"], kind)
		}
		for _, f := range []string{"label", "full", "shard"} {
			if s, _ := e[f].(string); s == "" {
				t.Errorf("%s has no %s", id, f)
			}
		}
	}
	fn := cat["shop-app:Shop.Orders.total/1"]
	if fn["parent"] != "shop-app:Shop.Orders" || fn["shard"] != "shop-app:Shop.Orders" || fn["label"] != "total/1" || fn["full"] != "Shop.Orders.total/1" {
		t.Errorf("function entry: %v", fn)
	}
	if m := cat["shop-app:Shop.Orders"]; m["count"].(float64) != 3 || m["icon"] != "elixir" || m["label"] != "Orders" {
		t.Errorf("module entry: %v", m)
	}
	if a := cat["ann:checkout"]; len(list(a["members"])) != 3 || a["status"] != "needs_review" {
		t.Errorf("annotation entry: %v", a)
	}
}

func TestModuleShard(t *testing.T) {
	srv := newServer(t, true)
	// A function id resolves to its module's shard; ids contain "/" and ":".
	d := getJSON[obj](t, srv, "/api/entity/"+url.PathEscape("shop-app:Shop.Orders.total/1"))
	if d["id"] != "shop-app:Shop.Orders" {
		t.Fatalf("shard id %v", d["id"])
	}
	fns := asObj(d["functions"])
	if len(fns) != 3 {
		t.Fatalf("functions: %d", len(fns))
	}
	total := asObj(fns["shop-app:Shop.Orders.total/1"])
	in := list(total["in"])
	if len(in) != 2 || asObj(in[0])["sites"].(float64) != 1 || asObj(in[0])["precision"] != "precise" {
		t.Errorf("total in: %v", in)
	}
	if total["url"] != "https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/orders.ex#L20" {
		t.Errorf("url %v", total["url"])
	}
	create := asObj(fns["shop-app:Shop.Orders.create/2"])
	out := list(create["out"])
	if len(out) != 1 || asObj(out[0])["sites"].(float64) != 2 {
		t.Errorf("create out: %v", out)
	}
	if tb := list(create["tables"]); len(tb) != 1 || tb[0] != "public.orders" {
		t.Errorf("create tables: %v", tb)
	}
	if len(list(create["annotations"])) != 1 || len(list(create["docs"])) != 1 {
		t.Errorf("create docs/annotations: %v %v", create["docs"], create["annotations"])
	}
	// Edges agree in both directions across shards.
	billing := asObj(asObj(getJSON[obj](t, srv, "/api/entity/shop-app:Shop.Billing")["functions"])["shop-app:Shop.Billing.charge/2"])
	if o := list(billing["out"]); len(o) != 1 || asObj(o[0])["id"] != "shop-app:Shop.Orders.total/1" {
		t.Errorf("charge out: %v", o)
	}
	if billing["external_or_unresolved_sites"].(float64) != 2 {
		t.Errorf("charge unresolved: %v", billing["external_or_unresolved_sites"])
	}
	worker := asObj(asObj(getJSON[obj](t, srv, "/api/entity/shop-worker:Worker.Nightly")["functions"])["shop-worker:Worker.Nightly.run/0"])
	if o := asObj(list(worker["out"])[0]); o["precision"] != "heuristic" {
		t.Errorf("cross-repo edge precision: %v", o)
	}
}

func TestTableShard(t *testing.T) {
	srv := newServer(t, true)
	d := getJSON[obj](t, srv, "/api/entity/public.orders")
	if d["name"] != "orders" || d["schema"] != "public" || len(list(d["columns"])) != 3 {
		t.Errorf("table: %v", d)
	}
	rel := asObj(list(d["relationships"])[0])
	if rel["from"] != "public.orders" || rel["to"] != "public.customers" || rel["kind"] != "physical_fk" {
		t.Errorf("relationship: %v", rel)
	}
	ref := asObj(list(d["references"])[0])
	if ref["function"] != "shop-app:Shop.Orders.create/2" || ref["precision"] != "candidate" || !strings.Contains(ref["url"].(string), "/blob/aaaaaaa") {
		t.Errorf("reference: %v", ref)
	}
	if idx := asObj(list(d["indexes"])[0]); idx["primary"] != true {
		t.Errorf("primary key index: %v", idx)
	}
	for _, k := range []string{"constraints", "triggers", "sources"} {
		if _, ok := d[k].([]any); !ok {
			t.Errorf("%s must be an array: %v", k, d[k])
		}
	}
}

func TestOtherEntities(t *testing.T) {
	srv := newServer(t, true)
	c := getJSON[obj](t, srv, "/api/entity/ctx:shop-app:Shop.Orders")
	if c["kind"] != "context" || len(list(c["modules"])) != 1 || len(list(c["tables"])) != 1 || len(list(c["docs"])) != 1 {
		t.Errorf("context: %v", c)
	}
	a := getJSON[obj](t, srv, "/api/entity/ann:checkout")
	if a["kind"] != "journey" || len(list(a["steps"])) != 2 {
		t.Errorf("annotation: %v", a)
	}
	s := getJSON[obj](t, srv, "/api/entity/s:shop-app")
	if s["kind"] != "service" || s["url"] != "https://github.com/example/shop-app" {
		t.Errorf("service: %v", s)
	}
	get(t, srv, "/api/entity/nope", 404)
}

func TestFilesSchemaOverview(t *testing.T) {
	srv := newServer(t, true)
	files := getJSON[obj](t, srv, "/api/files")
	repos := list(files["repositories"])
	if len(repos) != 2 {
		t.Fatalf("repositories: %v", repos)
	}
	app := asObj(repos[0])
	if app["id"] != "shop-app" || app["count"].(float64) != 5 || app["revision"] != strings.Repeat("a", 40) {
		t.Errorf("shop-app: %v", app)
	}
	var orders obj
	for _, f := range list(app["files"]) {
		if asObj(f)["path"] == "lib/shop/orders.ex" {
			orders = asObj(f)
		}
	}
	if orders == nil || orders["functions"].(float64) != 3 || len(list(orders["modules"])) != 1 || orders["category"] != "Source" || orders["id"] != "file:shop-app:lib/shop/orders.ex" {
		t.Errorf("orders.ex: %v", orders)
	}

	schema := getJSON[[]obj](t, srv, "/api/schema")
	if len(schema) != 2 || schema[1]["id"] != "public.orders" || len(list(schema[1]["columns"])) != 3 || schema[1]["relationships"].(float64) != 1 {
		t.Errorf("schema: %v", schema)
	}

	ov := getJSON[obj](t, srv, "/api/overview")
	m := asObj(ov["manifest"])
	if m["snapshot"] != snap || asObj(m["sources"])["shop-worker"] != strings.Repeat("b", 40) || len(list(m["coverage"])) < 3 {
		t.Errorf("manifest: %v", m)
	}
	home := asObj(asObj(ov["views"])["home"])
	if len(list(home["nodes"])) != 3 || len(list(home["edges"])) != 2 {
		t.Errorf("home view: %v", home)
	}
	if len(list(ov["contexts"])) != 5 || len(list(ov["services"])) != 3 {
		t.Errorf("contexts/services: %v %v", ov["contexts"], ov["services"])
	}
	for _, k := range []string{"changes", "evidence"} {
		if l, ok := ov[k].([]any); !ok || len(l) != 0 {
			t.Errorf("%s should be an empty array", k)
		}
	}
}

func TestAnnotationsStatusSnapshots(t *testing.T) {
	srv := newServer(t, true)
	anns := getJSON[[]obj](t, srv, "/api/annotations")
	if len(anns) != 1 {
		t.Fatalf("annotations: %v", anns)
	}
	a := anns[0]
	for _, k := range []string{"kind", "id", "title", "summary", "body", "cites", "reviewed", "steps", "status"} {
		if _, ok := a[k]; !ok {
			t.Errorf("annotation lacks %s", k)
		}
	}
	steps := list(a["steps"])
	place, charge := asObj(steps[0]), asObj(steps[1])
	if place["status"] != "needs_review" || list(place["reasons"])[0] != "changed: shop-app:Shop.Orders.create/2" || len(list(place["cites"])) != 2 {
		t.Errorf("place: %v", place)
	}
	if charge["status"] != "unreviewed" {
		t.Errorf("charge: %v", charge)
	}

	st := getJSON[obj](t, srv, "/api/status")
	if st["snapshot"] != snap || st["generated_at"] != "2026-10-15T13:00:00Z" || asObj(st["freshness"])["summary"] != "shop-app 2 commits behind" {
		t.Errorf("status: %v", st)
	}
	if asObj(st["counts"])["functions"].(float64) != 7 || !strings.HasPrefix(st["provenance"].(string), "map "+snap) {
		t.Errorf("status counts: %v", st)
	}

	snaps := getJSON[[]obj](t, srv, "/api/snapshots")
	if len(snaps) != 1 || snaps[0]["id"] != snap || len(list(snaps[0]["repos"])) != 2 {
		t.Errorf("snapshots: %v", snaps)
	}
	getJSON[obj](t, srv, "/api/status?snapshot="+snap)
	get(t, srv, "/api/status?snapshot=nope", 404)
	get(t, srv, "/api/nothing", 404)
}

func TestEmbedMermaidGraph(t *testing.T) {
	srv := newServer(t, true)
	md, h := get(t, srv, "/api/embed?id="+url.QueryEscape("Shop.Billing.charge/2"), 200)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/markdown") || !strings.Contains(md, "```mermaid\ngraph LR") || !strings.Contains(md, "map "+snap) {
		t.Errorf("embed:\n%s", md)
	}
	mm, _ := get(t, srv, "/api/mermaid?max=2&id="+url.QueryEscape("Shop.Orders.total/1"), 200)
	if !strings.HasPrefix(mm, "graph LR") || !strings.Contains(mm, "+1 more") {
		t.Errorf("mermaid:\n%s", mm)
	}
	get(t, srv, "/api/embed?id=zzzz", 404)
	g := getJSON[obj](t, srv, "/api/graph?depth=2&id="+url.QueryEscape("Shop.Billing.charge/2"))
	depths := map[string]float64{}
	for _, n := range list(g["nodes"]) {
		depths[asObj(n)["id"].(string)] = asObj(n)["depth"].(float64)
	}
	if depths["shop-app:Shop.Billing.charge/2"] != 0 || depths["shop-worker:Worker.Nightly.run/0"] != -1 || depths["shop-app:Shop.Orders.total/1"] != 1 || len(list(g["edges"])) != 3 {
		t.Errorf("graph: %v", g)
	}
}

func TestStaticUI(t *testing.T) {
	srv := newServer(t, true)
	html, _ := get(t, srv, "/", 200)
	for _, want := range []string{`src="app.js"`, `id="snapshot-picker"`, `id="save-pdf"`, `id="download-svg"`, `id="copy-embed"`, `data-view="contexts"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	for _, banned := range []string{"refresh.js", "Generate in GitHub", "<base"} {
		if strings.Contains(html, banned) {
			t.Errorf("index.html still has admin-only %q", banned)
		}
	}
	js, h := get(t, srv, "/app.js", 200)
	if !strings.Contains(h.Get("Content-Type"), "javascript") || strings.Contains(js, "data/") {
		t.Errorf("app.js: %s, loads static data files: %v", h.Get("Content-Type"), strings.Contains(js, "data/"))
	}
	get(t, srv, "/ICON-LICENSE.txt", 200)
	get(t, srv, "/assets/elixir.svg", 200)
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP: %q", csp)
	}
}

func TestNoSnapshot(t *testing.T) {
	srv := newServer(t, false)
	body, _ := get(t, srv, "/api/catalog", 404)
	if !strings.Contains(body, "cartographer pull") {
		t.Errorf("no-snapshot message: %s", body)
	}
	if s := getJSON[[]obj](t, srv, "/api/snapshots"); len(s) != 0 {
		t.Errorf("snapshots: %v", s)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	srv := newServer(t, true)
	req, _ := http.NewRequest("GET", srv.URL+"/api/status", nil)
	req.Host = "evil.example:7878"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: status %d", res.StatusCode)
	}
	req.Host = "localhost:7878"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("localhost Host: status %d", res.StatusCode)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "", "::", "192.0.2.10", "example.com"} {
		if ln, err := Listen(host, 0); err == nil {
			ln.Close()
			t.Errorf("Listen(%q) bound %s; want refusal", host, ln.Addr())
		}
	}
	for _, host := range []string{"127.0.0.1", "localhost"} {
		ln, err := Listen(host, 0)
		if err != nil {
			t.Fatalf("Listen(%q): %v", host, err)
		}
		if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
			t.Errorf("Listen(%q) bound %s", host, ln.Addr())
		}
		ln.Close()
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	ln, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, http.NotFoundHandler()) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

// TestJSContracts runs the UI's node tests; the artifact and journey
// contracts fetch the API from this server.
func TestJSContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; run `mise exec -- go test ./...` to include the UI tests")
	}
	srv := newServer(t, true)
	tests, _ := filepath.Glob("../../web/scripts/*.test.js")
	if len(tests) == 0 {
		t.Fatal("no UI tests found")
	}
	cmd := exec.Command(node, append([]string{"--test"}, tests...)...)
	cmd.Env = append(os.Environ(), "CARTOGRAPHER_URL="+srv.URL+"/", "CARTOGRAPHER_FIXTURE="+fixture)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
	if m := regexp.MustCompile(`skipped (\d+)`).FindStringSubmatch(string(out)); m == nil || m[1] != "0" {
		t.Errorf("UI contract tests were skipped despite CARTOGRAPHER_URL:\n%s", out)
	}
}
