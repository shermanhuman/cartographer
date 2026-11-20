package project_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/userconfig"
)

// buildElsewhere builds the registered project with its own state, data
// and cache directories and its own clock, returning the map id.
func (x *h) buildElsewhere(at time.Time) string {
	x.t.Helper()
	other := x.t.TempDir()
	s, err := store.Open(filepath.Join(other, "carto.db"))
	if err != nil {
		x.t.Fatal(err)
	}
	defer s.Close()
	reg, err := project.Load(x.state, "shop")
	if err != nil {
		x.t.Fatal(err)
	}
	state := filepath.Join(other, "state")
	if err := project.Save(state, reg); err != nil {
		x.t.Fatal(err)
	}
	r := x.runner()
	r.Store, r.StateDir, r.CacheDir = s, state, filepath.Join(other, "cache")
	r.Mise.Dir = state
	r.Now = func() time.Time { return at }
	res, err := r.Cycle(context.Background(), "shop", true)
	if err != nil {
		x.t.Fatal(err)
	}
	si, _, _ := s.Info(context.Background(), res.Snapshot)
	return si.MapID
}

// Two builds of the same origins at two clock times, with separate data
// and cache directories, give the same map id; a changed fact, doc or
// annotation changes it, and so does renaming a doc with the same content.
func TestMapIDStableAcrossClockAndDirs(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	a := x.buildElsewhere(base)
	b := x.buildElsewhere(base.Add(7 * time.Hour))
	if a == "" || a != b {
		t.Fatalf("map ids %s and %s", a, b)
	}
	seen := map[string]string{a: "base"}
	for _, c := range []struct {
		what  string
		repo  string
		files map[string]string
	}{
		{"a fact", "sales", map[string]string{"module.txt": "Renamed"}},
		{"a doc", "admin", map[string]string{"README.md": "# Admin, edited\n"}},
		{"an annotation", "admin", map[string]string{".cartographer/annotations/notes.json": `{"annotations":[{"kind":"note","id":"n1","title":"A note"}]}`}},
		{"a doc added", "sales", map[string]string{"guide.md": "# Guide\n"}},
		{"a doc renamed, same content", "sales", map[string]string{"guide.md": "", "manual.md": "# Guide\n"}},
	} {
		x.push(c.repo, c.files)
		id := x.buildElsewhere(base)
		if prev, ok := seen[id]; ok {
			t.Errorf("%s: map id unchanged (same as after %s)", c.what, prev)
		}
		seen[id] = c.what
	}
}

// Two repos with annotation files of the same name and distinct ids both
// load; a duplicate id across repos fails.
func TestAnnotationsSameNameAcrossRepos(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	ann := func(id string) string {
		return `{"annotations":[{"kind":"note","id":"` + id + `","title":"` + id + `"}]}`
	}
	x.push("admin", map[string]string{".cartographer/annotations/notes.json": ann("admin-note")})
	x.push("sales", map[string]string{".cartographer/annotations/notes.json": ann("sales-note")})
	x.register(sales)
	r := x.runner()
	res, err := r.Cycle(context.Background(), "shop", true)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	x.s.DB.QueryRow(`SELECT count(*) FROM annotations WHERE snapshot_id = ?`, res.Snapshot).Scan(&n)
	if n != 2 {
		t.Fatalf("%d annotations, want 2", n)
	}
	x.push("sales", map[string]string{".cartographer/annotations/notes.json": ann("admin-note")})
	if _, err := r.Cycle(context.Background(), "shop", true); err == nil || !strings.Contains(err.Error(), `duplicate annotation id "admin-note" in admin/notes.json and sales/notes.json`) {
		t.Fatalf("want the duplicate id, got %v", err)
	}
}

// A member's repo.toml changing on its main is used by the next build with
// no prompt; a different repo.toml in a feature-branch checkout doesn't
// change the id; --head uses the working copy and is labelled "your
// checkouts".
func TestSettingsChangeOnMainAndHead(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	facts := func(id string) string {
		m, err := x.s.Manifest(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(m.Facts, " ")
	}
	x.push("sales", map[string]string{".cartographer/repo.toml": settings("shop", "acme/admin", "extract = []")})
	x.advance(15 * time.Minute)
	res, err := r.Cycle(context.Background(), "shop", false)
	if err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if f := facts(res.Snapshot); strings.Contains(f, "sales.elixir") {
		t.Fatalf("the new repo.toml wasn't used: %s", f)
	}
	_, id := x.mapped()

	git(t, sales, "pull", "-q", "--ff-only", filepath.Join(x.dir, "origins", "sales.git"), "main")
	git(t, sales, "switch", "-q", "-c", "try-settings")
	writeFiles(t, sales, map[string]string{".cartographer/repo.toml": settings("shop", "acme/admin", `extract = ["elixir"]`, `docs = false`)})
	git(t, sales, "commit", "-q", "-am", "local settings")
	if res, err := r.Cycle(context.Background(), "shop", true); err != nil || res.Imported {
		t.Fatalf("a feature-branch repo.toml rebuilt: %+v %v", res, err)
	}
	if _, id2 := x.mapped(); id2 != id {
		t.Fatal("map id changed")
	}

	// --head: the working copy, settings included, into its own store.
	writeFiles(t, sales, map[string]string{"module.txt": "WorkingCopy"})
	hs, err := store.Open(filepath.Join(t.TempDir(), "head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	hr := x.runner()
	hr.Store = hs
	hres, err := hr.Head(context.Background(), sales)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := hs.Manifest(context.Background(), hres.Snapshot)
	if m.Source != "head" || !strings.Contains(strings.Join(m.Facts, " "), "sales.elixir") {
		t.Fatalf("head build: source %q facts %v", m.Source, m.Facts)
	}
	var mod int
	hs.DB.QueryRow(`SELECT count(*) FROM modules WHERE snapshot_id = ? AND name = 'WorkingCopy'`, hres.Snapshot).Scan(&mod)
	if mod != 1 {
		t.Error("the working copy's uncommitted file wasn't mapped")
	}
	sess := &session.Session{Store: hs, Cwd: sales, StateDir: x.state, Now: x.clock}
	q, _, err := sess.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h := header(t, q); !strings.Contains(h, "your checkouts") {
		t.Errorf("head header: %s", h)
	}
}

// Build entered twice with one schema cache runs prepare and capture once.
func TestSchemaCachedRunsOnce(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	log := filepath.Join(x.dir, "schema.log")
	schema := `{"facts_version":2,"producer":{"name":"schema","version":"1"},"tables":[{"id":"table:orders","name":"orders"}]}`
	x.push("admin", map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`,
			"[schema]", `prepare = "echo prepare >> `+log+`"`, `cache_key = ["priv/migrations"]`,
			`capture = ["sh", "-c", "echo capture >> `+log+`; cat {root}/schema.json"]`),
		"priv/migrations/001.exs": "create table(:orders)\n",
		"schema.json":             schema,
	})
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	x.push("admin", map[string]string{"lib/other.ex": "x\n"}) // outside cache_key
	res, err := r.Cycle(context.Background(), "shop", true)
	if err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(log)
	if got := string(b); got != "prepare\ncapture\n" {
		t.Fatalf("schema step ran:\n%s", got)
	}
	var tables int
	x.s.DB.QueryRow(`SELECT count(*) FROM db_tables WHERE snapshot_id = ?`, res.Snapshot).Scan(&tables)
	if tables != 1 {
		t.Errorf("%d tables", tables)
	}
	x.push("admin", map[string]string{"priv/migrations/002.exs": "alter\n"})
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); strings.Count(string(b), "capture") != 2 {
		t.Errorf("a cache_key change didn't rerun the step:\n%s", b)
	}
}

// legacySnapshot imports a snapshot of workspace "shop" that isn't a
// project build: source "" (published) or a legacy build, created later
// than any main one, mapping sales at rev.
func (x *h) legacySnapshot(source, rev string) string {
	x.t.Helper()
	dir := x.t.TempDir()
	sec := map[string]string{"": "00", "head": "01"}[source]
	m := map[string]any{
		"snapshot_format": 2, "id": "snapshot-20261125T0000" + sec + "Z-8d9001d3", "created_at": "2026-11-25T00:00:" + sec + "Z", "workspace": "shop",
		"repos": []map[string]string{{"name": "sales", "url": "https://github.com/acme/sales", "revision": rev}},
		"facts": []string{"facts/sales.json"},
	}
	if source != "" {
		m["source"] = source
	}
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)
	os.MkdirAll(filepath.Join(dir, "facts"), 0o755)
	os.WriteFile(filepath.Join(dir, "facts", "sales.json"), []byte(`{"facts_version":2,"producer":{"name":"legacy","version":"1"},"repo":"sales","modules":[{"id":"sales:Legacy","name":"Legacy","file":"lib/m.ex"}]}`), 0o644)
	id, err := x.s.Import(context.Background(), dir)
	if err != nil {
		x.t.Fatal(err)
	}
	return id
}

// The Mac case: a legacy local_workspaces entry and a project of the same
// name. The project's main snapshots are chosen and the legacy one doesn't
// build; after project remove the legacy one does.
func TestLegacyWorkspaceOfSameNameIgnored(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	first := git(t, sales, "rev-parse", "HEAD")
	writeFiles(t, sales, map[string]string{"lib/local.ex": "x\n"})
	git(t, sales, "add", ".")
	git(t, sales, "commit", "-q", "-m", "local") // ahead of the legacy map
	ws := filepath.Join(x.dir, "legacy-map")
	writeFiles(t, ws, map[string]string{"cartographer.toml": "name = \"shop\"\n[[repos]]\nname = \"sales\"\nurl = \"https://github.com/acme/sales\"\npath = \"" + sales + "\"\n"})
	cfg := filepath.Join(x.dir, "config.toml")
	os.WriteFile(cfg, nil, 0o644)
	t.Setenv("CARTOGRAPHER_CONFIG", cfg)
	if err := userconfig.AddLocalWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	legacy := x.legacySnapshot("", first)
	x.register(sales)
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	main, ok, _ := x.s.Newest(context.Background(), "shop", "main")
	if !ok {
		t.Fatal("no main snapshot")
	}
	command := func() (string, int) {
		legacyBuilds := 0
		sess := &session.Session{Store: x.s, DataDir: filepath.Dir(x.db), DB: x.db, Cwd: sales, StateDir: x.state, Now: x.clock,
			Rebuild:        func(ws, dir string) { legacyBuilds++ },
			ProjectRebuild: func(string) {}}
		id, err := sess.Choose(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Fresh(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		return id, legacyBuilds
	}
	if id, n := command(); id != main.ID || n != 0 {
		t.Fatalf("chose %s (want main %s), legacy builds %d", id, main.ID, n)
	}
	if err := project.Remove(x.state, "shop"); err != nil {
		t.Fatal(err)
	}
	if id, n := command(); id != legacy || n != 1 {
		t.Fatalf("after project remove: chose %s (want legacy %s), legacy builds %d", id, legacy, n)
	}
}

// A newer published snapshot of a registered project's name is never
// chosen.
func TestPublishedSnapshotOfProjectNeverChosen(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	x.legacySnapshot("", git(t, sales, "rev-parse", "HEAD"))
	x.legacySnapshot("head", git(t, sales, "rev-parse", "HEAD"))
	if x.snapshots() != 3 {
		t.Fatalf("%d snapshots", x.snapshots())
	}
	main, _, _ := x.s.Newest(context.Background(), "shop", "main")
	for _, cwd := range []string{sales, t.TempDir()} {
		sess := &session.Session{Store: x.s, Cwd: cwd, StateDir: x.state, Now: x.clock}
		if id, err := sess.Choose(context.Background()); err != nil || id != main.ID {
			t.Errorf("from %s chose %s, %v; want %s", cwd, id, err, main.ID)
		}
	}
}
