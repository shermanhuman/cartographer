package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
)

// eventBundle: two repos. web has two LiveViews (Sign and Start) that
// mount the same hook object under two names, a LiveView whose catch-all
// delegates to Report, and a hook mounted where no clause takes its event.
// other has a hook of the same name, never linked from web's JS.
func eventBundle() *bundle.Bundle {
	b := emptyBundle("snapshot-E", "shop", "2026-11-23T00:00:00Z")
	doc := func(repo string) *facts.Doc {
		if !slices.ContainsFunc(b.Manifest.Repos, func(r bundle.Repo) bool { return r.Name == repo }) {
			b.Manifest.Repos = append(b.Manifest.Repos, bundle.Repo{Name: repo, URL: "https://github.com/acme/" + repo, Revision: "r"})
		}
		d := &facts.Doc{FactsVersion: 3, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: repo}
		b.Facts = append(b.Facts, d)
		return d
	}
	fn := func(id, file string) facts.Function {
		return facts.Function{ID: id, Name: id, Arity: 1, MinArity: 1, File: file, Line: 1}
	}

	ex := doc("web")
	for _, id := range []string{"web:Sign.render/1", "web:Sign.handle_event/3", "web:Start.render/1", "web:Start.handle_event/3",
		"web:Rev.render/1", "web:Rev.handle_event/3", "web:Report.handle_event/3", "web:Quiet.render/1", "web:Quiet.handle_event/3"} {
		ex.Functions = append(ex.Functions, fn(id, "lib/web.ex"))
	}
	ex.EventHandlers = []facts.EventHandler{
		{Function: "web:Sign.handle_event/3", Event: "signed", Source: "head", Line: 10},
		{Function: "web:Sign.handle_event/3", Event: "save", Source: "head", Line: 12},
		{Function: "web:Start.handle_event/3", Event: "signed", Source: "guard", Line: 20},
		{Function: "web:Rev.handle_event/3", Source: "delegate", Delegate: "web:Report.handle_event/3", Line: 30},
		{Function: "web:Report.handle_event/3", Event: "export", Source: "head", Line: 40},
		{Function: "web:Quiet.handle_event/3", Event: "other", Source: "head", Line: 50},
	}
	ex.HookMounts = []facts.HookMount{
		{Hook: "DocumentSignature", From: "web:Sign.render/1", Receivers: []string{"web:Sign.handle_event/3"}, File: "lib/sign.ex", Line: 5},
		{Hook: "PasskeyAssertion", From: "web:Start.render/1", Receivers: []string{"web:Start.handle_event/3"}, File: "lib/start.ex", Line: 6},
		{Hook: "DocumentSignature", From: "web:Quiet.render/1", Receivers: []string{"web:Quiet.handle_event/3"}, File: "lib/quiet.ex", Line: 7},
	}
	ex.Events = []facts.Event{
		{From: "web:Sign.render/1", Event: "save", Via: "phx-click", To: []string{"web:Sign.handle_event/3"}, File: "lib/sign.ex", Line: 4, Precision: facts.Precise},
		{From: "web:Rev.render/1", Event: "export", Via: "phx-click", To: []string{"web:Report.handle_event/3", "web:Rev.handle_event/3"}, File: "lib/rev.ex", Line: 8, Precision: facts.Heuristic},
		{From: "web:Quiet.render/1", Event: "nobody", Via: "phx-click", File: "lib/quiet.ex", Line: 9, Precision: facts.Heuristic},
	}

	js := doc("web")
	js.Files = []facts.File{{Path: "assets/js/sig.js"}}
	js.Functions = []facts.Function{fn("web:assets/js/sig.js:anonymous@6:38/1", "assets/js/sig.js")}
	js.Events = []facts.Event{
		{From: "web:assets/js/sig.js:anonymous@6:38/1", Event: "signed", Via: "pushEvent", Hooks: []string{"DocumentSignature", "PasskeyAssertion"}, File: "assets/js/sig.js", Line: 15, Precision: facts.Heuristic},
		{From: "web:assets/js/sig.js:anonymous@6:38/1", Dynamic: true, Via: "pushEvent", Hooks: []string{"DocumentSignature"}, File: "assets/js/sig.js", Line: 16},
		// pushEventTo goes wherever its selector points: never linked.
		{From: "web:assets/js/sig.js:anonymous@6:38/1", Event: "signed", Via: "pushEventTo", Target: "dynamic", Hooks: []string{"DocumentSignature"}, File: "assets/js/sig.js", Line: 17},
	}

	other := doc("other")
	other.Functions = []facts.Function{fn("other:Sign.render/1", "lib/o.ex"), fn("other:Sign.handle_event/3", "lib/o.ex")}
	other.EventHandlers = []facts.EventHandler{{Function: "other:Sign.handle_event/3", Event: "signed", Source: "head", Line: 3}}
	other.HookMounts = []facts.HookMount{{Hook: "DocumentSignature", From: "other:Sign.render/1", Receivers: []string{"other:Sign.handle_event/3"}, File: "lib/o.ex", Line: 2}}
	return b
}

func eventLinks(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT from_id, to_id, event, precision, provenance, to_line FROM event_links ORDER BY from_id, to_id, event`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var from, to, ev, prec, prov string
		var line int
		rows.Scan(&from, &to, &ev, &prec, &prov, &line)
		out = append(out, from+" -> "+to+" "+ev+" "+prec+" "+prov+" @"+itoa(line))
	}
	return out
}

func itoa(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }

// Template sends link as the extractor resolved them; a hook's pushEvent
// links through every mount of any of its registry names, in its own repo,
// to receivers with a clause for the name; a dynamic name links nothing.
func TestEventLinks(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ImportBundle(context.Background(), eventBundle()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"web:Rev.render/1 -> web:Report.handle_event/3 export heuristic  @40",
		"web:Rev.render/1 -> web:Rev.handle_event/3 export heuristic  @30",
		"web:Sign.render/1 -> web:Sign.handle_event/3 save precise  @12",
		"web:assets/js/sig.js:anonymous@6:38/1 -> web:Sign.handle_event/3 signed heuristic phx-hook DocumentSignature (lib/sign.ex:5) @10",
		"web:assets/js/sig.js:anonymous@6:38/1 -> web:Start.handle_event/3 signed heuristic phx-hook PasskeyAssertion (lib/start.ex:6) @20",
	}
	if got := eventLinks(t, s); !slices.Equal(got, want) {
		t.Errorf("event links:\n got %q\nwant %q", got, want)
	}
	// Every send is kept, linked or not, for the coverage counts.
	var sends, edges int
	s.DB.QueryRow(`SELECT count(*) FROM event_sends`).Scan(&sends)
	s.DB.QueryRow(`SELECT count(*) FROM edges WHERE kind = 'event'`).Scan(&edges)
	if sends != 6 || edges != 5 {
		t.Errorf("sends %d (want 6), event edges %d (want 5)", sends, edges)
	}
}

// A 0.2.0 store (schema 3, without the event tables) is rebuilt on open,
// and then takes event links.
func TestOpenRebuildsAPre03Store(t *testing.T) {
	p := filepath.Join(t.TempDir(), "carto.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportBundle(context.Background(), emptyBundle("snapshot-old", "W", "2026-10-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP VIEW edges`, `DROP TABLE event_links`, `DROP TABLE event_sends`, `ALTER TABLE http_refs DROP COLUMN origin`,
		`CREATE VIEW edges AS SELECT snapshot_id, from_id, to_id, kind, precision, repo, file, line FROM calls`, `PRAGMA user_version = 3`} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()

	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v, snaps int
	s.DB.QueryRow(`PRAGMA user_version`).Scan(&v)
	s.DB.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&snaps)
	if v != schemaVersion || snaps != 0 {
		t.Fatalf("after open: user_version %d (want %d), snapshots %d (want 0)", v, schemaVersion, snaps)
	}
	if err := s.ImportBundle(context.Background(), eventBundle()); err != nil {
		t.Fatal(err)
	}
	if got := eventLinks(t, s); len(got) != 5 {
		t.Errorf("event links after rebuild: %q", got)
	}
}
