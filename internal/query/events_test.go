package query

import (
	"context"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
	"github.com/shermanhuman/cartographer/internal/store"
)

// newQEvents: one repo, web, with a LiveView whose template sends two events
// to its handle_event, a hook's pushEvent linked through a mount, a dynamic
// send, a clause nothing reaches, a same-origin request and one that links
// nowhere, and a template that didn't parse.
func newQEvents(t *testing.T) *Q {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	b := &bundle.Bundle{Manifest: bundle.Manifest{SnapshotFormat: bundle.Format, ID: "snapshot-ev", Workspace: "shop", CreatedAt: "2026-11-23T00:00:00Z",
		Contexts: bundle.ContextCfg{Depth: 2}, Repos: []bundle.Repo{{Name: "web", URL: "https://github.com/acme/web", Revision: "r"}}}}
	fn := func(id, file string) facts.Function {
		return facts.Function{ID: id, Name: id, Arity: 1, MinArity: 1, File: file, Line: 1}
	}
	ex := &facts.Doc{FactsVersion: 3, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: "web",
		Functions: []facts.Function{fn("web:Page.render/1", "lib/page.ex"), fn("web:Page.handle_event/3", "lib/page.ex"), fn("web:Page.show/2", "lib/page.ex")},
		EventHandlers: []facts.EventHandler{
			{Function: "web:Page.handle_event/3", Event: "save", Source: "head", Line: 10},
			{Function: "web:Page.handle_event/3", Event: "delete", Source: "head", Line: 11},
			{Function: "web:Page.handle_event/3", Event: "signed", Source: "head", Line: 12},
			{Function: "web:Page.handle_event/3", Event: "dead", Source: "head", Line: 13},
			{Function: "web:Page.handle_event/3", Event: "save", Source: "head", Line: 14}, // a fallback clause
		},
		HookMounts: []facts.HookMount{{Hook: "Pad", From: "web:Page.render/1", Receivers: []string{"web:Page.handle_event/3"}, File: "lib/page.ex", Line: 4}},
		Events: []facts.Event{
			{From: "web:Page.render/1", Event: "save", Via: "phx-click", To: []string{"web:Page.handle_event/3"}, File: "lib/page.ex", Line: 5, Precision: facts.Precise},
			{From: "web:Page.render/1", Event: "delete", Via: "phx-click", To: []string{"web:Page.handle_event/3"}, File: "lib/page.ex", Line: 6, Precision: facts.Precise},
			{From: "web:Page.render/1", Dynamic: true, Via: "phx-change", File: "lib/page.ex", Line: 7},
		},
		Routes: []facts.Route{{ID: "web:route:R:GET /dial/me", Method: "GET", Path: "/dial/me", Handler: "web:Page.show/2", File: "lib/router.ex", Line: 3}},
		Notes:  []facts.Note{{Kind: "heex_parse_error", File: "lib/broken.ex", Message: "line 3: expected closing \" for attribute value"}},
	}
	js := &facts.Doc{FactsVersion: 3, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: "web",
		Functions: []facts.Function{fn("web:assets/js/pad.js:Pad.mounted/0", "assets/js/pad.js"), fn("web:assets/js/app.js:start/0", "assets/js/app.js")},
		Events:    []facts.Event{{From: "web:assets/js/pad.js:Pad.mounted/0", Event: "signed", Via: "pushEvent", Hooks: []string{"Pad"}, File: "assets/js/pad.js", Line: 3, Precision: facts.Heuristic}},
		HTTPRefs: []facts.HTTPRef{
			{From: "web:assets/js/app.js:start/0", Method: "GET", Path: "/dial/me", Origin: "same", File: "assets/js/app.js", Line: 8},
			{From: "web:assets/js/app.js:start/0", Method: "GET", Path: "/nowhere", Origin: "same", File: "assets/js/app.js", Line: 9},
		},
		Stats: []facts.Stats{{Language: "http", CallSites: 3, Refs: 2, NotExtracted: 1}},
	}
	b.Facts = []*facts.Doc{ex, js}
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	q, err := New(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// callers on a handle_event names each sender and the events it sends;
// callers on a route lists its same-origin requests.
func TestCallersShowEvents(t *testing.T) {
	q, ctx := newQEvents(t), context.Background()
	out := render(t)(q.Calls(ctx, "web:Page.handle_event/3", 1, true, 20, 0))
	has(t, out,
		`d1 FN web:Page.render/1  lib/page.ex:1  [precise event: phx-click "delete", "save"] ×2`,
		`d1 FN web:assets/js/pad.js:Pad.mounted/0  assets/js/pad.js:1  [heuristic event: pushEvent "signed" via phx-hook Pad (lib/page.ex:4)]`)
	out = render(t)(q.Calls(ctx, "web:route:R:GET /dial/me", 1, true, 20, 0))
	has(t, out, `d1 FN web:assets/js/app.js:start/0  assets/js/app.js:1  [heuristic http: same-origin]`)
}

// Coverage has an events row (sends linked, dynamic, unlinked; clauses
// reached) and an http row (requests linked, no path), and status lists the
// extractors' notes.
func TestEventAndHTTPCoverage(t *testing.T) {
	q, ctx := newQEvents(t), context.Background()
	cov, err := q.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ev, http *Coverage
	for i := range cov {
		switch cov[i].Language {
		case "events":
			ev = &cov[i]
		case "http":
			http = &cov[i]
		}
	}
	if ev == nil || ev.Sends != 4 || ev.SendsLinked != 3 || ev.SendsDynamic != 1 || ev.Handlers != 5 || ev.HandlersLinked != 4 {
		t.Errorf("events row %+v: want 3 of 4 sends linked, 1 dynamic, 4 of 5 clauses", ev)
	}
	if http == nil || http.HTTPRefs != 2 || http.HTTPLinked != 1 || http.NotExtracted != 1 {
		t.Errorf("http row %+v: want 1 of 2 linked, 1 with no path", http)
	}
	out := render(t)(q.Status(ctx))
	has(t, out,
		"web                        events     3 of 4 sends linked (75%) · dynamic 1 · unlinked 0 · 4 of 5 handle_event clauses reached (80%)",
		"web                        http       1 of 2 requests linked to a route (50%) · no path 1",
		"handle_event names no send reaches",
		`web                        Page "dead" (lib/page.ex:13)`,
		"extractor notes",
		"web                        heex_parse_error ×1  first: lib/broken.ex")
}
