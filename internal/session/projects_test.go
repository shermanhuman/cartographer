package session

import (
	"context"
	"errors"
	"github.com/shermanhuman/cartographer/internal/testfixture"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// projectSession is a session over a file store with alpha registered to it,
// beta registered to another store, and a corrupt registration.
func projectSession(t *testing.T) (*Session, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "carto.db")
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(state, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*project.Registration{
		{Name: "alpha", Anchor: "acme/alpha-admin", Store: db, Members: []project.Member{{Repo: "acme/alpha-admin"}, {Repo: "acme/alpha-web"}}},
		{Name: "beta", Anchor: "acme/beta", Store: filepath.Join(dir, "other.db"), Members: []project.Member{{Repo: "acme/beta"}}},
	} {
		if err := project.Save(state, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "projects", "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	sess := &Session{Store: s, DB: db, Cwd: dir, StateDir: state, ProjectRebuild: func(string) { started.Add(1) }}
	return sess, &started
}

func TestProjectSnapshotFirstBuildAndUnknown(t *testing.T) {
	sess, started := projectSession(t)
	ctx := context.Background()
	_, err := sess.ProjectSnapshot(ctx, "alpha")
	var first *ErrFirstBuild
	if !errors.As(err, &first) || !strings.Contains(first.Status, "has started") {
		t.Fatalf("never-built project: %v, want ErrFirstBuild saying its build has started", err)
	}
	if n := started.Load(); n != 1 {
		t.Errorf("ProjectRebuild ran %d times, want 1", n)
	}
	if nf, ok := err.(interface{ NotFound() bool }); !ok || !nf.NotFound() {
		t.Errorf("ErrFirstBuild isn't a 404 for the server")
	}
	var unknown *ErrUnknownProject
	if _, err := sess.ProjectSnapshot(ctx, "nope"); !errors.As(err, &unknown) {
		t.Errorf("unknown project: %v, want ErrUnknownProject", err)
	}
	// beta is registered to another store, so it isn't a project here.
	if _, err := sess.ProjectSnapshot(ctx, "beta"); !errors.As(err, &unknown) {
		t.Errorf("another store's project: %v, want ErrUnknownProject", err)
	}
}

func TestProjectSnapshotNewestMainOnly(t *testing.T) {
	sess, started := projectSession(t)
	ctx := context.Background()
	importAs(t, sess.Store, "alpha", "snapshot-20261016T120000Z", "")
	// importAs keeps the mini fixture's format-1 manifest, which has no
	// source: it imports as published, so alpha still has no main build.
	if _, err := sess.ProjectSnapshot(ctx, "alpha"); err == nil {
		t.Fatal("a published snapshot was taken as alpha's map")
	}
	sess.memo = nil
	v2 := testfixture.V2As(t, "alpha", "snapshot-20261121T000000Z", "main")
	if _, err := sess.Store.Import(ctx, v2); err != nil {
		t.Fatal(err)
	}
	before := started.Load()
	id, err := sess.ProjectSnapshot(ctx, "alpha")
	if err != nil || id != "snapshot-20261121T000000Z" {
		t.Fatalf("ProjectSnapshot = %q, %v; want the main build", id, err)
	}
	if started.Load() != before {
		t.Error("a hit started a build")
	}
}

func TestProjectsList(t *testing.T) {
	sess, _ := projectSession(t)
	ctx := context.Background()
	if _, err := sess.Store.Import(ctx, testfixture.V2As(t, "alpha", "snapshot-20261121T000000Z", "main")); err != nil {
		t.Fatal(err)
	}
	l, err := sess.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Projects) != 1 || l.Projects[0].Name != "alpha" {
		t.Fatalf("projects = %+v, want alpha only (beta is another store's)", l.Projects)
	}
	p := l.Projects[0]
	if p.LastSnapshot != "snapshot-20261121T000000Z" || strings.Join(p.Members, ",") != "acme/alpha-admin,acme/alpha-web" || p.LastCycle.State != "never" {
		t.Errorf("alpha = %+v", p)
	}
	if len(l.Problems) != 1 || !strings.HasSuffix(l.Problems[0].File, "broken.json") {
		t.Errorf("problems = %+v, want broken.json", l.Problems)
	}
}

func TestProjectsRegistryErrors(t *testing.T) {
	sess, _ := projectSession(t)
	ctx := context.Background()
	sess.StateDir = filepath.Join(t.TempDir(), "missing")
	if l, err := sess.Projects(ctx); err != nil || len(l.Projects) != 0 || l.Problems == nil {
		t.Errorf("missing registry: %+v, %v; want empty lists", l, err)
	}
	// A registry path that isn't a directory can't be read: an error, not
	// "no projects".
	bad := filepath.Join(t.TempDir(), "state")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "projects"), []byte("x"), 0o644)
	sess.StateDir = bad
	if _, err := sess.Projects(ctx); err == nil {
		t.Error("an unreadable registry listed as no projects")
	}
}
