package project_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
)

// snapshotCheckout records everything of a checkout a cycle must not
// change: the work tree's files, the index, HEAD, every ref outside
// refs/cartographer/ and FETCH_HEAD.
func snapshotCheckout(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			h.Write([]byte(rel + "\x00"))
			h.Write(b)
		}
		return nil
	})
	gitDir := git(t, dir, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	for _, f := range []string{"index", "FETCH_HEAD", "HEAD"} {
		b, _ := os.ReadFile(filepath.Join(gitDir, f))
		h.Write([]byte(f + "\x00"))
		h.Write(b)
	}
	for _, line := range strings.Split(git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if !strings.HasPrefix(line, "refs/cartographer/") {
			h.Write([]byte(line + "\n"))
		}
	}
	h.Write([]byte(git(t, dir, "status", "--porcelain", "--ignored")))
	return hex.EncodeToString(h.Sum(nil))
}

// A checkout on a feature branch, ahead with uncommitted files: its
// commits spawn no rebuild, the map shows the fetched commit, nothing of
// the checkout changes, and the header shows branch, ahead/behind and
// uncommitted counts. A clean checkout behind the map shows none
// uncommitted.
func TestFeatureBranchCheckoutIsLeftAlone(t *testing.T) {
	x := newH(t)
	admin, sales, _ := x.world()
	// The admin checkout's own remote is the very URL fetched (through the
	// redirect): its refs/remotes/origin/* still don't move.
	git(t, admin, "remote", "set-url", "origin", "https://github.com/acme/admin")
	git(t, sales, "switch", "-q", "-c", "fix/dialer")
	for _, f := range []string{"a.ex", "b.ex"} {
		writeFiles(t, sales, map[string]string{"lib/" + f: "x\n"})
		git(t, sales, "add", ".")
		git(t, sales, "commit", "-q", "-m", f)
	}
	git(t, sales, "tag", "local-tag")
	writeFiles(t, sales, map[string]string{"lib/m.ex": "edited\n", "scratch.txt": "untracked\n"})
	for _, co := range []string{admin, sales} {
		gd := filepath.Join(co, ".git")
		os.WriteFile(filepath.Join(gd, "FETCH_HEAD"), []byte("sentinel\n"), 0o644)
	}
	want := x.push("sales", map[string]string{"lib/up.ex": "upstream\n"})
	wantAdmin := x.push("admin", map[string]string{"lib/up.ex": "upstream\n"})
	before := map[string]string{admin: snapshotCheckout(t, admin), sales: snapshotCheckout(t, sales)}

	x.register(sales)
	if r := x.command(sales); r.cycles != 1 || r.err != nil {
		t.Fatalf("%+v", r)
	}
	revs, _ := x.mapped()
	if revs["sales"] != want || revs["admin"] != wantAdmin {
		t.Fatalf("mapped %v, want sales %s admin %s", revs, want, wantAdmin)
	}
	for co, b := range before {
		if snapshotCheckout(t, co) != b {
			t.Errorf("%s changed", co)
		}
		if got, _ := os.ReadFile(filepath.Join(co, ".git", "FETCH_HEAD")); string(got) != "sentinel\n" {
			t.Errorf("%s FETCH_HEAD = %q", co, got)
		}
	}
	// More local commits: still nothing to build.
	writeFiles(t, sales, map[string]string{"lib/c.ex": "x\n"})
	git(t, sales, "add", "lib/c.ex")
	git(t, sales, "commit", "-q", "-m", "c")
	x.advance(time.Minute)
	r := x.command(sales)
	if r.cycles != 0 || r.err != nil {
		t.Fatalf("local commits spawned a rebuild: %+v", r)
	}
	if !strings.Contains(r.header, "your checkout: fix/dialer, 3 ahead, 1 behind, 2 uncommitted, ") {
		t.Errorf("header: %s", r.header)
	}
	r = x.command(admin)
	if !strings.Contains(r.header, "your checkout: main, 1 behind") || strings.Contains(r.header, "uncommitted") {
		t.Errorf("clean checkout behind the map: %s", r.header)
	}
}

// The checkout's own remote is unreachable; https://github.com/<owner>/<name>
// is redirected to the bare origin: the map has the bare origin's commit, so
// the fetch used the URL derived from owner/name.
func TestFetchUsesGitHubURLNotCheckoutRemote(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	git(t, sales, "remote", "add", "fork", "git@github.com:someone/sales.git")
	other := filepath.Join(x.dir, "elsewhere.git")
	git(t, x.dir, "init", "-q", "--bare", other)
	git(t, sales, "remote", "set-url", "--add", "origin", other)
	c := x.push("sales", map[string]string{"lib/n.ex": "n\n"})
	x.register(sales)
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	if revs, _ := x.mapped(); revs["sales"] != c {
		t.Fatalf("mapped %s, want the origin's %s", revs["sales"], c)
	}
}

// Against a server that answers 401, a credential helper that answers
// nothing, and an inherited GIT_ASKPASS/SSH_ASKPASS: the helper was asked,
// the inherited prompt never ran, and the fetch fails within its budget.
func TestFetch401AsksHelperNeverInheritedAskpass(t *testing.T) {
	x := newH(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	co := filepath.Join(x.root, "locked")
	os.MkdirAll(co, 0o755)
	git(t, co, "init", "-q")
	git(t, co, "remote", "add", "origin", "git@github.com:acme/locked.git")
	helperLog := filepath.Join(x.dir, "helper.log")
	helper := filepath.Join(x.dir, "helper.sh")
	os.WriteFile(helper, []byte("#!/bin/sh\necho \"$1\" >> '"+helperLog+"'\ncat > /dev/null\n"), 0o755)
	sentinel := filepath.Join(x.dir, "sentinel")
	prompt := filepath.Join(x.dir, "prompt.sh")
	os.WriteFile(prompt, []byte("#!/bin/sh\necho ran >> '"+sentinel+"'\necho secret\n"), 0o755)
	git(t, x.dir, "config", "--file", x.gitcfg, "url."+srv.URL+"/locked.git.insteadOf", "https://github.com/acme/locked")
	git(t, x.dir, "config", "--file", x.gitcfg, "credential.helper", "!"+helper)
	t.Setenv("GIT_ASKPASS", prompt)
	t.Setenv("SSH_ASKPASS", prompt)
	old := project.FetchBudget
	project.FetchBudget = 20 * time.Second
	defer func() { project.FetchBudget = old }()
	start := time.Now()
	_, err := x.runner().Plan(context.Background(), co)
	if err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("want a fetch failure, got %v", err)
	}
	if d := time.Since(start); d > project.FetchBudget {
		t.Errorf("took %s", d)
	}
	if b, _ := os.ReadFile(helperLog); !strings.Contains(string(b), "get") {
		t.Errorf("credential helper not asked: %q", b)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the inherited askpass ran")
	}
}

// A hanging origin with a pending change in another repo: fetching stays
// within its budget, the build uses the last fetched commit and still
// completes, the header shows the failure, and commands don't re-fetch
// before 15 minutes.
func TestHangingOriginWithinBudget(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	before, _ := x.mapped()
	var hits atomic.Int32
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)
	git(t, x.dir, "config", "--file", x.gitcfg, "--remove-section", "url."+filepath.Join(x.dir, "origins", "sales.git"))
	git(t, x.dir, "config", "--file", x.gitcfg, "url."+srv.URL+"/sales.git.insteadOf", "https://github.com/acme/sales")
	pending := x.push("admin", map[string]string{"lib/p.ex": "p\n"})
	old := project.FetchBudget
	project.FetchBudget = 2 * time.Second
	defer func() { project.FetchBudget = old }()
	x.advance(15 * time.Minute)
	start := time.Now()
	res := x.command(sales)
	if d := time.Since(start); d > project.FetchBudget+10*time.Second {
		t.Fatalf("took %s", d)
	}
	if res.cycles != 1 || res.err != nil {
		t.Fatalf("%+v", res)
	}
	revs, _ := x.mapped()
	if revs["admin"] != pending || revs["sales"] != before["sales"] {
		t.Fatalf("mapped %v; want admin %s and sales still %s", revs, pending, before["sales"])
	}
	if !strings.Contains(res.header, "sales fetch failed") || !strings.Contains(res.header, "using its last fetched commit "+before["sales"][:7]) {
		t.Errorf("header: %s", res.header)
	}
	n := hits.Load()
	for range 3 {
		x.advance(4 * time.Minute)
		if r := x.command(sales); r.cycles != 0 {
			t.Fatalf("re-fetched before 15 minutes")
		}
	}
	if hits.Load() != n {
		t.Errorf("origin hit %d more times", hits.Load()-n)
	}
}

// From an unrelated directory and from a worktree of a member: both build
// from the recorded checkouts, and only the "your work" part differs.
func TestUnrelatedDirAndWorktreeSameMap(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.register(sales)
	unrelated := filepath.Join(x.dir, "unrelated")
	os.MkdirAll(unrelated, 0o755)
	git(t, unrelated, "init", "-q")
	if r := x.command(unrelated); r.cycles != 1 {
		t.Fatalf("%+v", r)
	}
	_, id := x.mapped()
	wt := filepath.Join(x.dir, "elsewhere", "sales-wt")
	git(t, sales, "worktree", "add", "-q", "-b", "wt-branch", wt)
	writeFiles(t, wt, map[string]string{"lib/m.ex": "worktree edit\n"})
	x.advance(16 * time.Minute)
	r := x.command(wt)
	if r.cycles != 1 || r.err != nil {
		t.Fatalf("%+v", r)
	}
	if _, id2 := x.mapped(); id2 != id || x.snapshots() != 1 {
		t.Fatalf("map id %s -> %s (%d snapshots)", id, id2, x.snapshots())
	}
	if !strings.Contains(r.header, "your checkout: wt-branch, 1 uncommitted, 1 files differ from the map") {
		t.Errorf("worktree header: %s", r.header)
	}
	plain := x.command(t.TempDir())
	if strings.Contains(plain.header, "your checkout") {
		t.Errorf("outside any checkout: %s", plain.header)
	}
	strip := func(s string) string {
		i := strings.Index(s, " · your checkout:")
		if i < 0 {
			return s
		}
		rest := s[i+len(" · your checkout:"):]
		if j := strings.Index(rest, " · "); j >= 0 {
			return s[:i] + rest[j:]
		}
		return s[:i]
	}
	if strip(r.header) != strip(plain.header) {
		t.Errorf("headers differ beyond your work:\n%s\n%s", r.header, plain.header)
	}
}

// Members that don't line up, and an anchor never fetched, fail closed,
// each with its own message; nothing is imported.
func TestMembersThatDontLineUpFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(x *h)
		want   string
	}{
		{"another project", func(x *h) {
			x.push("sales", map[string]string{".cartographer/repo.toml": settings("other", "acme/admin")})
		}, `names project "other", not "shop"`},
		{"another anchor", func(x *h) {
			x.push("sales", map[string]string{".cartographer/repo.toml": settings("shop", "acme/infra")})
		}, "names anchor acme/infra, not acme/admin"},
		{"no .cartographer", func(x *h) {
			x.push("sales", map[string]string{".cartographer/repo.toml": ""})
		}, "no .cartographer/repo.toml on its fetched branch: run `cartographer project init " + "SALES"},
		{"malformed", func(x *h) {
			x.push("sales", map[string]string{".cartographer/repo.toml": "project = \n"})
		}, "settings: .cartographer/repo.toml: toml: line 1"},
		{"unknown key", func(x *h) {
			x.push("sales", map[string]string{".cartographer/repo.toml": settings("shop", "acme/admin", "colour = 1")})
		}, "unknown keys [colour]"},
		{"anchor never fetched", func(x *h) {
			os.RemoveAll(filepath.Join(x.dir, "origins", "admin.git"))
			git(x.t, filepath.Join(x.root, "admin"), "update-ref", "-d", "refs/cartographer/main")
		}, "acme/admin: fetch: never fetched successfully"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newH(t)
			_, sales, _ := x.world()
			x.register(sales)
			if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
				t.Fatal(err)
			}
			n := x.snapshots()
			tc.break_(x)
			want := strings.ReplaceAll(tc.want, "SALES", sales)
			_, err := x.runner().Cycle(context.Background(), "shop", true)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
			if x.snapshots() != n {
				t.Error("imported")
			}
			if st := x.state0(); st.Failed == nil || st.Failed.Log == "" {
				t.Errorf("failure not recorded with its log: %+v", st.Failed)
			}
		})
	}
}

// An unreachable anchor with an earlier fetch builds from its last fetched
// commit, and the header shows the fetch failure.
func TestUnreachableAnchorWithEarlierFetch(t *testing.T) {
	x := newH(t)
	admin, sales, _ := x.world()
	x.register(sales)
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	last := git(t, admin, "rev-parse", "refs/cartographer/main")
	os.RemoveAll(filepath.Join(x.dir, "origins", "admin.git"))
	c := x.push("sales", map[string]string{"lib/n.ex": "n\n"})
	x.advance(15 * time.Minute)
	r := x.command(sales)
	if r.cycles != 1 || r.err != nil {
		t.Fatalf("%+v", r)
	}
	if revs, _ := x.mapped(); revs["sales"] != c || revs["admin"] != last {
		t.Fatalf("mapped %v", revs)
	}
	if !strings.Contains(r.header, "admin fetch failed") {
		t.Errorf("header: %s", r.header)
	}
}

// A project build writes nothing inside any checkout.
func TestBuildWritesNothingInCheckouts(t *testing.T) {
	x := newH(t)
	admin, sales, infra := x.world()
	x.register(sales)
	before := map[string]string{}
	for _, co := range []string{admin, sales, infra} {
		before[co] = snapshotCheckout(t, co)
	}
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	for co, b := range before {
		if snapshotCheckout(t, co) != b {
			t.Errorf("%s changed: %s", co, git(t, co, "status", "--porcelain", "--ignored"))
		}
	}
	if left, _ := filepath.Glob(filepath.Join(project.Dir(x.state, "shop"), "build-*")); len(left) != 0 {
		t.Errorf("scratch left: %v", left)
	}
}
