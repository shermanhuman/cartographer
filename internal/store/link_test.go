package store

import "testing"

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
