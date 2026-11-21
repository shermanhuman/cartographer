package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// cliProject makes acme/solo, its own anchor with no extractors, behind
// a bare origin reached through url.insteadOf, and its checkout.
func cliProject(t *testing.T) (checkout, upstream string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = main\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CARTOGRAPHER_STATE", filepath.Join(dir, "state"))
	bare := filepath.Join(dir, "solo.git")
	os.MkdirAll(bare, 0o755)
	gitIn(t, bare, "init", "-q", "--bare", "-b", "main")
	gitIn(t, dir, "config", "--file", cfg, "url."+bare+".insteadOf", "https://github.com/acme/solo")
	upstream = filepath.Join(dir, "up")
	os.MkdirAll(filepath.Join(upstream, ".cartographer"), 0o755)
	os.WriteFile(filepath.Join(upstream, ".cartographer", "repo.toml"), []byte("project = \"solo\"\nmembers = [{repo = \"acme/solo\"}]\nextract = []\n"), 0o644)
	os.WriteFile(filepath.Join(upstream, "README.md"), []byte("# Solo\n"), 0o644)
	gitIn(t, upstream, "init", "-q", "-b", "main")
	gitIn(t, upstream, "add", ".")
	gitIn(t, upstream, "commit", "-qm", "init")
	gitIn(t, upstream, "push", "-q", bare, "HEAD:main")
	checkout = filepath.Join(dir, "github", "solo")
	gitIn(t, dir, "clone", "-q", bare, checkout)
	gitIn(t, checkout, "remote", "set-url", "origin", "git@github.com:acme/solo.git")
	return checkout, upstream
}

func TestProjectCommands(t *testing.T) {
	co, _ := cliProject(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	stdin = strings.NewReader("n\n")
	defer func() { stdin = os.Stdin }()
	var buf bytes.Buffer
	if err := run(context.Background(), []string{"project", "add", co, "--db", db}, &buf); err == nil || !strings.Contains(buf.String(), "Register project solo? [y/N]") {
		t.Fatalf("declined: %v\n%s", err, buf.String())
	}
	if regs, _ := project.List(project.StateDir()); len(regs) != 0 {
		t.Fatal("registered without a yes")
	}
	out := cli(t, db, "project", "add", co, "--yes")
	if !strings.Contains(out, "acme/solo") || !strings.Contains(out, co) || !strings.Contains(out, "registered solo") {
		t.Fatalf("add:\n%s", out)
	}
	if out := cli(t, db, "project", "list"); !strings.Contains(out, "solo (anchor acme/solo)") || !strings.Contains(out, "never built") {
		t.Errorf("list:\n%s", out)
	}
	if out := cli(t, db, "refresh"); !strings.Contains(out, "solo: imported map ") {
		t.Fatalf("refresh:\n%s", out)
	}
	if out := cli(t, db, "refresh"); !strings.Contains(out, "nothing to build (unchanged)") {
		t.Errorf("second refresh:\n%s", out)
	}
	t.Chdir(co)
	if out := cli(t, db, "status", "--no-pull"); !strings.Contains(out, "main as fetched") || !strings.Contains(out, "your checkout: main") || !strings.Contains(out, "project solo · map id ") {
		t.Errorf("status:\n%s", out)
	}
	if err := run(context.Background(), []string{"refresh", "--head", "--from", co}, &buf); err == nil || !strings.Contains(err.Error(), "its own --db") {
		t.Errorf("refresh --head without --db: %v", err)
	}
	if err := run(context.Background(), []string{"refresh", "--head", "--from", co, "--db", db}, &buf); err == nil || !strings.Contains(err.Error(), "project solo's store") {
		t.Errorf("refresh --head into the project's store: %v", err)
	}
	headDB := filepath.Join(t.TempDir(), "head.db")
	if out := cli(t, headDB, "refresh", "--head", "--from", co); !strings.Contains(out, "your checkouts: imported map") {
		t.Errorf("refresh --head:\n%s", out)
	}
	// review edits the annotation in the checkout it runs in.
	os.MkdirAll(filepath.Join(co, ".cartographer", "annotations"), 0o755)
	os.WriteFile(filepath.Join(co, ".cartographer", "annotations", "n.json"), []byte(`{"annotations":[{"kind":"note","id":"n1","title":"N","cites":[]}]}`), 0o644)
	if out := cli(t, db, "review", "n1"); !strings.Contains(out, "reviewed n1 against snapshot-") || !strings.Contains(out, "commit .cartographer/annotations") {
		t.Errorf("review:\n%s", out)
	}
	if out := cli(t, db, "project", "remove", "solo"); !strings.Contains(out, "removed project solo") {
		t.Errorf("remove:\n%s", out)
	}
	if regs, _ := project.List(project.StateDir()); len(regs) != 0 {
		t.Error("still registered")
	}
	// An unregistered project's --head map is labelled all the same.
	if out := cli(t, headDB, "status", "--no-pull"); !strings.Contains(out, "your checkouts, built ") {
		t.Errorf("head status, unregistered:\n%s", out)
	}
}

// project add owner/name without a checkout prints the clone command and
// doesn't register; project init names the registered project.
func TestProjectAddWithoutCheckoutAndInit(t *testing.T) {
	co, _ := cliProject(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	var buf bytes.Buffer
	err := run(context.Background(), []string{"project", "add", "acme/nowhere", "--yes", "--db", db}, &buf)
	if err == nil || !strings.Contains(err.Error(), "git clone https://github.com/acme/nowhere ") {
		t.Fatalf("%v", err)
	}
	if regs, _ := project.List(project.StateDir()); len(regs) != 0 {
		t.Fatal("registered")
	}
	cli(t, db, "project", "add", co, "--yes")
	os.Remove(filepath.Join(co, ".cartographer", "repo.toml"))
	out := cli(t, db, "project", "init", co)
	if !strings.Contains(out, "wrote "+filepath.Join(co, ".cartographer", "repo.toml")) || !strings.Contains(out, "git -C "+co+" switch -c cartographer-settings") {
		t.Errorf("init:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(co, ".cartographer", "repo.toml")); !strings.Contains(string(b), `project = "solo"`) {
		t.Errorf("init wrote:\n%s", b)
	}
}

// A one-shot query from the checkout spawns the detached refresh child,
// which builds the map; the next query answers from it.
func TestQuerySpawnsAProjectRefresh(t *testing.T) {
	spawns := realChild(t)
	co, up := cliProject(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "project", "add", co, "--yes")
	t.Chdir(co)
	var buf bytes.Buffer
	err := run(context.Background(), []string{"status", "--db", db, "--no-pull"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "no map of project solo yet") {
		t.Fatalf("first query: %v\n%s", err, buf.String())
	}
	sd := project.StateDir()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		st, _ := project.LoadState(sd, "solo")
		_, running := project.Lock(sd, "solo").Running("solo")
		if st.Imported != "" && !running {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(project.LogPath(sd, "solo"))
			t.Fatalf("the child didn't build; log:\n%s", b)
		}
	}
	if spawns() != 1 {
		t.Errorf("%d spawns", spawns())
	}
	if out := cli(t, db, "status", "--no-pull"); !strings.Contains(out, "main as fetched") {
		t.Errorf("status:\n%s", out)
	}
	// Within the cadence, nothing is spawned however the upstream moves.
	gitIn(t, up, "commit", "-q", "--allow-empty", "-m", "more")
	cli(t, db, "status", "--no-pull")
	if spawns() != 1 {
		t.Errorf("%d spawns within the cadence", spawns())
	}
}

// The web map's status for a registered project's map is stale only when
// something differs or failed, not because its header line is set.
func TestServeStatusOfProjectMap(t *testing.T) {
	co, _ := cliProject(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "project", "add", co, "--yes")
	cli(t, db, "refresh")
	t.Chdir(co)
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess := newSession(&common{db: db, noRebuild: true, noPull: true}, s, io.Discard)
	sess.Rebuild, sess.ProjectRebuild = nil, nil
	id, err := sess.Choose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status := func() map[string]any {
		v, err := serveStatus(sess)(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return v.(map[string]any)
	}
	if st := status(); st["stale"] != false || st["summary"] == "" {
		t.Errorf("a current project map: %v", st)
	}
	os.WriteFile(filepath.Join(co, "README.md"), []byte("# Changed\n"), 0o644)
	sess.Forget()
	if st := status(); st["stale"] != true {
		t.Errorf("a changed file: %v", st)
	}
}
