package store

import (
	"cmp"
	"context"
	"slices"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
)

func TestPathMatch(t *testing.T) {
	for _, c := range []struct {
		route, req string
		ok         bool
	}{
		{"/api/v1/sales/cold/check", "/api/v1/sales/cold/check", true},
		{"/api/v1/leads/:id", "/api/v1/leads/:p1", true},  // param to param
		{"/api/v1/leads/:id", "/api/v1/leads/42", true},   // a literal fills a route param
		{"/api/v1/leads/new", "/api/v1/leads/:p1", false}, // an interpolation never fills a literal
		{"/dial/{id}", "/dial/7", true},
		{"/files/{rest...}", "/files/a/b", true},
		{"/{$}", "/prices", false}, // {$} anchors "/" only
		{"/{$}", "/", true},
		{"/a/b", "/a", false},
		{"/a", "/a/b", false},
		{"/a/b", "/a/b?x=1", true},
		{"/files/*rest", "/files/a/b", true}, // Phoenix and axum globs take the rest
		{"/files/*rest", "/files", true},
		{"/files/{*rest}", "/files/a/b", true}, // axum 0.8
		{"/files/{rest...}", "/files", true},   // Go: the rest may be empty
		{"/files/*rest", "/other/a", false},
		{"/", "/", true},
	} {
		if got := pathMatch(c.route, c.req, false); got != c.ok {
			t.Errorf("pathMatch(%q, %q) = %v, want %v", c.route, c.req, got, c.ok)
		}
	}
	// A Go pattern ending in / is a subtree; "/" alone stays exact.
	for _, c := range []struct {
		route, req string
		ok         bool
	}{
		{"/static/", "/static/app.js", true},
		{"/static/", "/static/css/a.css", true},
		{"/static/", "/static", true},
		{"/static/", "/other/x", false},
		{"/", "/anything", false},
		{"/static/{$}", "/static/x", false},
	} {
		if got := pathMatch(c.route, c.req, true); got != c.ok {
			t.Errorf("Go pathMatch(%q, %q) = %v, want %v", c.route, c.req, got, c.ok)
		}
	}
}

// linkCase is one linker world: sales (one ref to refPath, "/check" by
// default, and a config URL), admin and billing (routes, in files named
// file, "router.ex" by default) and infra (svc's Deployment and Service in
// namespace ns; svc defaults to admin).
type linkCase struct {
	name, url, ns      string
	refPath, file, svc string
	origin, base       string
	routes             map[string][]string
	want               []string
}

// linksOf imports c's bundle and lists the links as "from -> route
// precision".
func linksOf(t *testing.T, c linkCase) []string {
	t.Helper()
	salesURL, ns := c.url, c.ns
	refPath, file, svc := cmp.Or(c.refPath, "/check"), cmp.Or(c.file, "router.ex"), cmp.Or(c.svc, "admin")
	routes := c.routes
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := emptyBundle("snapshot-L", "shop", "2026-11-20T00:00:00Z")
	doc := func(repo string) *facts.Doc {
		b.Manifest.Repos = append(b.Manifest.Repos, bundle.Repo{Name: repo, URL: "https://github.com/acme/" + repo, Revision: "r"})
		d := &facts.Doc{FactsVersion: 3, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: repo}
		b.Facts = append(b.Facts, d)
		return d
	}
	sales := doc("sales")
	sales.Modules = []facts.Module{{ID: "sales:M", Name: "M", File: "m.go"}}
	sales.Functions = []facts.Function{{ID: "sales:M.f/0", Module: "sales:M", Name: "f", File: "m.go", Line: 1}}
	sales.HTTPRefs = []facts.HTTPRef{{From: "sales:M.f/0", Method: "POST", Path: refPath, Base: c.base, Origin: c.origin, File: "m.go", Line: 2}}
	if salesURL != "" {
		sales.ConfigURLs = []facts.ConfigURL{{Key: "cold.admin_url", Value: salesURL, File: "config.yaml", Line: 1}}
	}
	for _, repo := range []string{"admin", "billing"} {
		d := doc(repo)
		for _, p := range routes[repo] {
			d.Routes = append(d.Routes, facts.Route{ID: repo + ":route:R:POST " + p, Method: "POST", Path: p, File: file, Line: 1})
		}
	}
	if p := routes["sales"]; len(p) > 0 {
		sales.Routes = []facts.Route{{ID: "sales:route:R:POST " + p[0], Method: "POST", Path: p[0], File: file, Line: 1}}
	}
	infra := doc("infra")
	infra.Services = []facts.Service{
		{Kind: "deployment", Name: svc, Namespace: ns, Image: "ghcr.io/acme/" + svc + ":1", Labels: map[string]string{"app": svc}, File: "k8s.yaml", Line: 1},
		{Kind: "service", Name: svc, Namespace: ns, Selector: map[string]string{"app": svc}, File: "k8s.yaml", Line: 9},
	}
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB.Query(`SELECT from_id, route_id, precision FROM http_links ORDER BY route_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var from, to, prec string
		rows.Scan(&from, &to, &prec)
		out = append(out, from+" -> "+to+" "+prec)
	}
	return out
}

// The linker's choices: evidence through a Service beats a lone path
// match, two repos matching gives no link, a repo never links to itself
// (by a path match or through its own Service), the Service must be in the
// URL's namespace, and a Go route ending in / takes its subtree.
func TestLinkChoices(t *testing.T) {
	for _, c := range []linkCase{
		{name: "heuristic over candidate", url: "http://admin.shop.svc/api", ns: "shop",
			routes: map[string][]string{"admin": {"/api/check"}, "billing": {"/check"}},
			want:   []string{"sales:M.f/0 -> admin:route:R:POST /api/check heuristic"}},
		{name: "two repos match: no link", ns: "shop",
			routes: map[string][]string{"admin": {"/check"}, "billing": {"/check"}}},
		{name: "never to itself", ns: "shop",
			routes: map[string][]string{"sales": {"/check"}, "billing": {"/check"}},
			want:   []string{"sales:M.f/0 -> billing:route:R:POST /check candidate"}},
		{name: "never to itself through its own Service", url: "http://sales.shop.svc/api", ns: "shop", svc: "sales",
			routes: map[string][]string{"sales": {"/api/check"}, "billing": {"/other"}}},
		{name: "service in another namespace: no evidence", url: "http://admin.shop.svc/api", ns: "other",
			routes: map[string][]string{"admin": {"/api/check"}},
			want:   []string{"sales:M.f/0 -> admin:route:R:POST /api/check candidate"}},
		{name: "a Go route's subtree", ns: "shop", refPath: "/static/app.js", file: "router.go",
			routes: map[string][]string{"admin": {"/static/"}},
			want:   []string{"sales:M.f/0 -> admin:route:R:POST /static/ candidate"}},
		{name: "an Elixir route is no subtree", ns: "shop", refPath: "/static/app.js",
			routes: map[string][]string{"admin": {"/static/"}}},
		// A browser request with no host: its own repo, never another, even
		// when another repo's route matches or a Service points there.
		{name: "same-origin: its own repo", url: "http://admin.shop.svc", ns: "shop", origin: "same",
			routes: map[string][]string{"sales": {"/check"}, "admin": {"/check"}},
			want:   []string{"sales:M.f/0 -> sales:route:R:POST /check heuristic"}},
		{name: "same-origin: never another repo", ns: "shop", origin: "same",
			routes: map[string][]string{"admin": {"/check"}}},
		// Decision 17: a request to an outside host is never matched, even
		// when exactly one repo has the path; the project's own host is.
		{name: "an outside host: never linked", ns: "shop", base: "https://api.stripe.com",
			routes: map[string][]string{"admin": {"/check"}}},
		{name: "the project's own host: linked as before", url: "https://app.shop.io", ns: "shop", base: "https://app.shop.io",
			routes: map[string][]string{"admin": {"/check"}},
			want:   []string{"sales:M.f/0 -> admin:route:R:POST /check candidate"}},
	} {
		if got := linksOf(t, c); !slices.Equal(got, c.want) {
			t.Errorf("%s: links %q, want %q", c.name, got, c.want)
		}
	}
}
