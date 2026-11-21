package store

import (
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

// linksOf imports a bundle of sales (one ref), admin and billing (routes
// built per case) and infra (admin's Deployment and Service in namespace
// ns), and lists the links as "from -> route precision".
func linksOf(t *testing.T, salesURL, ns string, routes map[string][]string) []string {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := emptyBundle("snapshot-L", "shop", "2026-11-20T00:00:00Z")
	doc := func(repo string) *facts.Doc {
		b.Manifest.Repos = append(b.Manifest.Repos, bundle.Repo{Name: repo, URL: "https://github.com/acme/" + repo, Revision: "r"})
		d := &facts.Doc{FactsVersion: 2, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: repo}
		b.Facts = append(b.Facts, d)
		return d
	}
	sales := doc("sales")
	sales.Modules = []facts.Module{{ID: "sales:M", Name: "M", File: "m.go"}}
	sales.Functions = []facts.Function{{ID: "sales:M.f/0", Module: "sales:M", Name: "f", File: "m.go", Line: 1}}
	sales.HTTPRefs = []facts.HTTPRef{{From: "sales:M.f/0", Method: "POST", Path: "/check", File: "m.go", Line: 2}}
	if salesURL != "" {
		sales.ConfigURLs = []facts.ConfigURL{{Key: "cold.admin_url", Value: salesURL, File: "config.yaml", Line: 1}}
	}
	for _, repo := range []string{"admin", "billing"} {
		d := doc(repo)
		for _, p := range routes[repo] {
			d.Routes = append(d.Routes, facts.Route{ID: repo + ":route:R:POST " + p, Method: "POST", Path: p, File: "router.ex", Line: 1})
		}
	}
	if p := routes["sales"]; len(p) > 0 {
		sales.Routes = []facts.Route{{ID: "sales:route:R:POST " + p[0], Method: "POST", Path: p[0], File: "router.ex", Line: 1}}
	}
	infra := doc("infra")
	infra.Services = []facts.Service{
		{Kind: "deployment", Name: "admin", Namespace: ns, Image: "ghcr.io/acme/admin:1", Labels: map[string]string{"app": "admin"}, File: "k8s.yaml", Line: 1},
		{Kind: "service", Name: "admin", Namespace: ns, Selector: map[string]string{"app": "admin"}, File: "k8s.yaml", Line: 9},
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
// match, two repos matching gives no link, a repo never links to itself,
// and the Service must be in the URL's namespace.
func TestLinkChoices(t *testing.T) {
	for _, c := range []struct {
		name, url, ns string
		routes        map[string][]string
		want          []string
	}{
		{"heuristic over candidate", "http://admin.shop.svc/api", "shop",
			map[string][]string{"admin": {"/api/check"}, "billing": {"/check"}},
			[]string{"sales:M.f/0 -> admin:route:R:POST /api/check heuristic"}},
		{"two repos match: no link", "", "shop",
			map[string][]string{"admin": {"/check"}, "billing": {"/check"}},
			nil},
		{"never to itself", "", "shop",
			map[string][]string{"sales": {"/check"}, "billing": {"/check"}},
			[]string{"sales:M.f/0 -> billing:route:R:POST /check candidate"}},
		{"service in another namespace: no evidence", "http://admin.shop.svc/api", "other",
			map[string][]string{"admin": {"/api/check"}},
			[]string{"sales:M.f/0 -> admin:route:R:POST /api/check candidate"}},
	} {
		if got := linksOf(t, c.url, c.ns, c.routes); !slices.Equal(got, c.want) {
			t.Errorf("%s: links %q, want %q", c.name, got, c.want)
		}
	}
}
