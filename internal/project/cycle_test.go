package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/rebuild"
)

// A pushed commit is mapped by the first command once the last fetch is
// 15 minutes old, not before; refresh maps one at once.
func TestPushedCommitMappedAfterCadenceAndRefreshAtOnce(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	if r := x.command(sales); r.cycles != 1 {
		t.Fatalf("first command: %+v", r)
	}
	revs, _ := x.mapped()
	first := revs["sales"]
	if first == "" {
		t.Fatal("nothing mapped")
	}
	c2 := x.push("sales", map[string]string{"lib/n.ex": "defmodule N do end\n"})
	x.advance(14 * time.Minute)
	if r := x.command(sales); r.err != nil || r.cycles != 0 {
		t.Fatalf("before the cadence: %+v", r)
	}
	if revs, _ := x.mapped(); revs["sales"] != first {
		t.Fatalf("mapped %s before the cadence", revs["sales"])
	}
	x.advance(time.Minute)
	if r := x.command(sales); r.err != nil || r.cycles != 1 {
		t.Fatalf("at the cadence: %+v", r)
	}
	if revs, _ := x.mapped(); revs["sales"] != c2 {
		t.Fatalf("mapped %s, want %s", revs["sales"], c2)
	}
	c3 := x.push("sales", map[string]string{"lib/o.ex": "defmodule O do end\n"})
	if res, err := x.runner().Cycle(context.Background(), "shop", true); err != nil || !res.Imported {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if revs, _ := x.mapped(); revs["sales"] != c3 {
		t.Fatalf("refresh mapped %s, want %s", revs["sales"], c3)
	}
}

// A cycle with nothing new imports nothing and adds no snapshot.
func TestNoChangeCycleImportsNothing(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	r := x.runner()
	if res, err := r.Cycle(context.Background(), "shop", true); err != nil || !res.Imported {
		t.Fatal(res, err)
	}
	n, runs := x.snapshots(), x.runs()
	x.advance(time.Hour)
	res, err := r.Cycle(context.Background(), "shop", false)
	if err != nil || res.Imported || res.Skipped != "unchanged" {
		t.Fatalf("%+v %v", res, err)
	}
	if x.snapshots() != n || x.runs() != runs {
		t.Errorf("snapshots %d -> %d, extractor runs %d -> %d", n, x.snapshots(), runs, x.runs())
	}
}

// A successful fetch then a failing extractor: nothing imported, the
// header and status name it, the digest waits 6 hours, a new commit goes
// at once, refresh retries.
func TestFailingPluginHeldSixHours(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	good, _ := x.mapped()
	bad := x.push("sales", map[string]string{"FAIL": "x"})
	x.advance(15 * time.Minute)
	n := x.snapshots()
	if _, err := r.Cycle(context.Background(), "shop", false); err == nil || !strings.Contains(err.Error(), "sales at "+bad[:7]+": extract elixir") {
		t.Fatalf("want the extractor failure, got %v", err)
	}
	if x.snapshots() != n {
		t.Fatal("a failed build imported")
	}
	res := x.command(sales)
	if res.err != nil {
		t.Fatal(res.err)
	}
	for _, want := range []string{"behind the fetched commits: sales failed at extract elixir at " + bad[:7], "boom in sales", "retry in 6 h or `cartographer refresh`", "(log: "} {
		if !strings.Contains(res.header, want) {
			t.Errorf("header lacks %q:\n%s", want, res.header)
		}
	}
	if !strings.Contains(strings.Join(res.status, "\n"), "sales failed at extract elixir") {
		t.Errorf("status:\n%s", strings.Join(res.status, "\n"))
	}
	if revs, _ := x.mapped(); revs["sales"] != good["sales"] {
		t.Error("the previous map should keep answering")
	}
	runs := x.runs()
	for _, step := range []time.Duration{15 * time.Minute, 5 * time.Hour} {
		x.advance(step)
		if res, err := r.Cycle(context.Background(), "shop", false); err != nil || res.Imported || x.runs() != runs {
			t.Fatalf("retried the failed digest early: %+v %v (runs %d)", res, err, x.runs())
		}
	}
	x.advance(46 * time.Minute) // 6 h 1 min after the failure
	if _, err := r.Cycle(context.Background(), "shop", false); err == nil || x.runs() == runs {
		t.Fatalf("not retried after 6 h: %v", err)
	}
	runs = x.runs()
	x.advance(15 * time.Minute)
	if _, err := r.Cycle(context.Background(), "shop", true); err == nil || x.runs() == runs {
		t.Fatal("refresh should retry at once")
	}
	fixed := x.push("sales", map[string]string{"FAIL": ""})
	x.advance(15 * time.Minute)
	if res, err := r.Cycle(context.Background(), "shop", false); err != nil || !res.Imported {
		t.Fatalf("a new commit should build at once: %+v %v", res, err)
	}
	if revs, _ := x.mapped(); revs["sales"] != fixed {
		t.Error("fixed commit not mapped")
	}
	if st := x.state0(); st.Failed != nil {
		t.Errorf("failure kept after an import: %+v", st.Failed)
	}
}

// The first-ever attempt failing (a fetch, then separately an extractor)
// isn't re-attempted by consecutive commands before 15 minutes.
func TestFirstAttemptFailureWaitsForCadence(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		x := newH(t)
		_, sales, _ := x.world()
		x.register(sales)
		// The anchor's origin vanishes before its first cycle, and the
		// fetch registration made is gone.
		os.RemoveAll(filepath.Join(x.dir, "origins", "admin.git"))
		git(t, filepath.Join(x.root, "admin"), "update-ref", "-d", "refs/cartographer/main")
		if r := x.command(sales); r.cycles != 1 || r.err == nil || !strings.Contains(r.err.Error(), "admin failed at fetch") {
			t.Fatalf("%+v", r)
		}
		x.advance(5 * time.Minute)
		for range 3 {
			if r := x.command(sales); r.cycles != 0 {
				t.Fatalf("re-attempted before 15 minutes: %+v", r)
			}
		}
		x.advance(10 * time.Minute)
		if r := x.command(sales); r.cycles != 1 {
			t.Fatalf("not re-attempted at 15 minutes: %+v", r)
		}
	})
	t.Run("plugin", func(t *testing.T) {
		x := newH(t)
		_, sales, _ := x.world()
		x.push("sales", map[string]string{"FAIL": "x"})
		x.register(sales)
		if r := x.command(sales); r.cycles != 1 || r.err == nil || !strings.Contains(r.err.Error(), "sales failed at extract elixir") {
			t.Fatalf("%+v", r)
		}
		runs := x.runs()
		x.advance(14 * time.Minute)
		if r := x.command(sales); r.cycles != 0 || x.runs() != runs {
			t.Fatalf("re-attempted: %+v", r)
		}
	})
}

// Two callers racing with no lock yet build once.
func TestRacingCallersBuildOnce(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	// Both callers saw "never attempted"; the first holds its cycle open
	// until the second has tried.
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer project.SetHookLocked(func() {
		entered <- struct{}{}
		<-release
	})()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = x.runner().Cycle(context.Background(), "shop", false)
		}()
		if i == 0 {
			<-entered
		}
	}
	// The second either is refused by the lock or (without one) enters too.
	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if !errors.Is(errs[0], rebuild.ErrRunning) && !errors.Is(errs[1], rebuild.ErrRunning) {
		t.Errorf("neither caller was refused by the lock: %v", errs)
	}
	// Two elixir members: one build runs the extractor twice.
	if x.runs() != 2 || x.snapshots() != 1 {
		t.Fatalf("runs %d, snapshots %d", x.runs(), x.snapshots())
	}
}

// An empty store with a registered project: the first command starts a
// build.
func TestEmptyStoreFirstCommandStartsBuild(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	if x.snapshots() != 0 {
		t.Fatal("store not empty")
	}
	r := x.command(t.TempDir()) // not even in a checkout
	if r.cycles != 1 || x.snapshots() != 1 {
		t.Fatalf("%+v, %d snapshots", r, x.snapshots())
	}
}

// A missing checkout fails closed with the clone command; nothing is
// cloned.
func TestMissingCheckoutFailsClosedWithCloneCommand(t *testing.T) {
	x := newH(t)
	_, sales, infra := x.world()
	x.register(sales)
	os.RemoveAll(infra)
	_, err := x.runner().Cycle(context.Background(), "shop", true)
	want := "git clone https://github.com/acme/infra " + filepath.Join(x.root, "infra")
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	if _, err := os.Stat(infra); err == nil {
		t.Fatal("something cloned infra")
	}
	if x.snapshots() != 0 {
		t.Fatal("imported")
	}
	if st := x.state0(); st.Failed == nil || st.Failed.Step != "checkout" || st.Failed.Repo != "acme/infra" {
		t.Errorf("failure: %+v", st.Failed)
	}
}

// The anchor's list gains a repo: the same build maps it, or fails closed
// with its clone command when there's no checkout.
func TestAnchorListGainsRepo(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	x.repo("extra", map[string]string{".cartographer/repo.toml": settings("shop", "acme/admin", `extract = ["elixir"]`), ".tool-versions": elixirPins})
	x.push("admin", map[string]string{".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}, {repo = "acme/extra"}]`, `extract = ["elixir"]`)})
	if res, err := r.Cycle(context.Background(), "shop", true); err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if revs, _ := x.mapped(); revs["extra"] == "" {
		t.Fatalf("extra not mapped: %v", revs)
	}
	reg, _ := project.Load(x.state, "shop")
	if m, ok := reg.Member("acme/extra"); !ok || m.Path != filepath.Join(x.root, "extra") {
		t.Errorf("extra not recorded: %+v", reg.Members)
	}

	x.repo("ghost", map[string]string{".cartographer/repo.toml": settings("shop", "acme/admin")})
	os.RemoveAll(filepath.Join(x.root, "ghost"))
	x.push("admin", map[string]string{".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}, {repo = "acme/extra"}, {repo = "acme/ghost"}]`, `extract = ["elixir"]`)})
	if _, err := r.Cycle(context.Background(), "shop", true); err == nil || !strings.Contains(err.Error(), "git clone https://github.com/acme/ghost") {
		t.Fatalf("want the clone command, got %v", err)
	}
}
