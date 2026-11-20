package project_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/session"
	"github.com/shermanhuman/cartographer/internal/store"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "carto-project-")
	os.Setenv("CARTOGRAPHER_CONFIG", filepath.Join(dir, "config.toml"))
	os.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	os.Setenv("CARTOGRAPHER_STATE", filepath.Join(dir, "state"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// h is a test world: GitHub stood in for by bare origins reached through
// url.<bare>.insteadOf, checkouts under a checkout root whose own remote
// is unreachable, a fake mise providing fake tools, and a clock.
type h struct {
	t        *testing.T
	dir      string
	root     string // checkout root (like ~/github)
	state    string
	cache    string
	tools    string // fake tool installs
	miseLog  string
	plugins  string
	db       string
	s        *store.Store
	mu       sync.Mutex
	now      time.Time
	upstream map[string]string // name -> working clone that pushes to the origin
	gitcfg   string
}

const owner = "acme"

func newH(t *testing.T) *h {
	t.Helper()
	x := &h{t: t, dir: t.TempDir(), now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), upstream: map[string]string{}}
	x.root = filepath.Join(x.dir, "github")
	x.state = filepath.Join(x.dir, "state")
	x.cache = filepath.Join(x.dir, "cache")
	x.tools = filepath.Join(x.dir, "tools")
	x.miseLog = filepath.Join(x.dir, "mise.log")
	x.plugins = filepath.Join(x.dir, "plugins")
	x.gitcfg = filepath.Join(x.dir, "gitconfig")
	for _, d := range []string{x.root, x.state, x.cache, x.tools, x.plugins} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(x.plugins, "README"), []byte("fake plugins\n"), 0o644)
	os.WriteFile(x.gitcfg, []byte("[protocol \"file\"]\n\tallow = always\n[init]\n\tdefaultBranch = main\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", x.gitcfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
	x.fakeMise()
	x.fakeTool("erlang@28.3.1", "erl", "#!/bin/sh\nexit 0\n")
	x.fakeTool("elixir@1.19.5-otp-28", "elixir", x.fakeElixir())
	x.db = filepath.Join(x.dir, "data", "carto.db")
	os.MkdirAll(filepath.Dir(x.db), 0o755)
	s, err := store.Open(x.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	x.s = s
	return x
}

func (x *h) clock() time.Time {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.now
}

func (x *h) advance(d time.Duration) {
	x.mu.Lock()
	x.now = x.now.Add(d)
	x.mu.Unlock()
}

func sanitize(spec string) string {
	return strings.NewReplacer(":", "_", "/", "_", "@", "_").Replace(spec)
}

// fakeMise answers where, install, bin-paths and env for tools under
// x.tools (install succeeds only for those under x.tools/available).
func (x *h) fakeMise() {
	script := `#!/bin/sh
echo "$*" >> '` + x.miseLog + `'
T='` + x.tools + `'
cmd=$1; shift
case $cmd in
where) s=$(echo "$1" | tr ':/@' '___'); [ -d "$T/$s" ] && echo "$T/$s" && exit 0; echo "$1 not installed" >&2; exit 1 ;;
install) s=$(echo "$1" | tr ':/@' '___'); [ -d "$T/available/$s" ] && mv "$T/available/$s" "$T/$s" && exit 0; echo "no such version $1" >&2; exit 1 ;;
bin-paths) s=$(echo "$1" | tr ':/@' '___'); echo "$T/$s/bin" ;;
env) echo '{"FAKE_MISE_ENV":"1"}' ;;
*) echo "fake mise: $cmd" >&2; exit 2 ;;
esac
`
	os.WriteFile(filepath.Join(x.dir, "mise"), []byte(script), 0o755)
}

func (x *h) fakeTool(spec, bin, body string) {
	d := filepath.Join(x.tools, sanitize(spec), "bin")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, bin), []byte(body), 0o755)
}

// fakeElixir is the counting fake extractor: it counts each run, fails when
// the archive has a FAIL file, and prints a facts document whose module
// is named by the archive's module.txt.
func (x *h) fakeElixir() string {
	return `#!/bin/sh
echo run >> '` + filepath.Join(x.dir, "elixir.count") + `'
echo "PATH=$PATH" >> '` + filepath.Join(x.dir, "elixir.env") + `'
shift
while [ $# -gt 0 ]; do
  case $1 in
  --repo) repo=$2; shift 2 ;;
  --root) root=$2; shift 2 ;;
  --revision) rev=$2; shift 2 ;;
  --schema-facts) schema=$2; shift 2 ;;
  *) shift 2 ;;
  esac
done
if [ -f "$root/FAIL" ]; then echo "boom in $repo" >&2; exit 3; fi
mod=$(cat "$root/module.txt" 2>/dev/null || echo Mod)
printf '{"facts_version":2,"producer":{"name":"fake-elixir","version":"1"},"repo":"%s","revision":"%s","files":[{"path":"lib/m.ex","language":"elixir","category":"source"}],"modules":[{"id":"%s:%s","name":"%s","file":"lib/m.ex"}]}\n' "$repo" "$rev" "$repo" "$mod" "$mod"
`
}

func (x *h) runs() int {
	b, _ := os.ReadFile(filepath.Join(x.dir, "elixir.count"))
	return strings.Count(string(b), "run")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if c == "" { // delete
			os.Remove(full)
			continue
		}
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// settings is a member's repo.toml.
func settings(project, anchor string, extra ...string) string {
	s := fmt.Sprintf("project = %q\n", project)
	if anchor != "" {
		s += fmt.Sprintf("anchor = %q\n", anchor)
	}
	return s + strings.Join(extra, "\n") + "\n"
}

const elixirPins = "erlang 28.3.1\nelixir 1.19.5-otp-28\n"

// repo makes acme/<name>: an origin (bare), an upstream clone that pushes
// to it, the insteadOf redirect of https://github.com/acme/<name> to it, and
// a checkout under the root whose own remote is unreachable.
func (x *h) repo(name string, files map[string]string) string {
	t := x.t
	bare := filepath.Join(x.dir, "origins", name+".git")
	os.MkdirAll(bare, 0o755)
	git(t, bare, "init", "-q", "--bare", "-b", "main")
	git(t, x.dir, "config", "--file", x.gitcfg, "url."+bare+".insteadOf", "https://github.com/"+owner+"/"+name)
	up := filepath.Join(x.dir, "upstream", name)
	os.MkdirAll(up, 0o755)
	git(t, up, "init", "-q", "-b", "main")
	git(t, up, "remote", "add", "origin", bare)
	x.upstream[name] = up
	x.push(name, files)
	co := filepath.Join(x.root, name)
	git(t, x.dir, "clone", "-q", bare, co)
	git(t, co, "remote", "set-url", "origin", "git@github.com:"+owner+"/"+name+".git")
	return co
}

// push commits files (""= delete) upstream and pushes; returns the commit.
func (x *h) push(name string, files map[string]string) string {
	up := x.upstream[name]
	writeFiles(x.t, up, files)
	git(x.t, up, "add", "-A")
	git(x.t, up, "commit", "-q", "--allow-empty", "-m", "change")
	git(x.t, up, "push", "-q", "origin", "HEAD:main")
	return git(x.t, up, "rev-parse", "HEAD")
}

// world makes the standard project "shop": anchor admin (elixir), sales
// (elixir) and infra (kubernetes), each with repo.toml, returning the
// checkouts.
func (x *h) world() (admin, sales, infra string) {
	admin = x.repo("admin", map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin", branch = "main"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`, `docs = ["README.md"]`),
		".tool-versions":          elixirPins,
		"lib/m.ex":                "defmodule M do end\n",
		"README.md":               "# Admin\n",
		"module.txt":              "Admin",
	})
	sales = x.repo("sales", map[string]string{
		".cartographer/repo.toml": settings("shop", "acme/admin", `extract = ["elixir"]`),
		".tool-versions":          elixirPins,
		"lib/m.ex":                "defmodule M do end\n",
		"config.yaml":             "cold:\n  admin_url: http://admin.shop.svc/api\n",
		"module.txt":              "Sales",
	})
	infra = x.repo("infra", map[string]string{
		".cartographer/repo.toml": settings("shop", "acme/admin", `extract = ["kubernetes"]`, `docs = false`),
		"k8s/admin.yaml":          "apiVersion: v1\nkind: Service\nmetadata:\n  name: admin\n  namespace: shop\nspec:\n  selector:\n    app: admin\n",
		"README.md":               "# Infra\n",
	})
	return
}

func (x *h) runner() *project.Runner {
	return &project.Runner{
		StateDir: x.state, Store: x.s, DB: x.db,
		PluginsDir: x.plugins, PluginsDigest: "fakeplugins", CacheDir: x.cache,
		Self: "/nonexistent/cartographer", Version: "test",
		Mise:  &project.Mise{Bin: filepath.Join(x.dir, "mise"), Dir: x.state},
		Roots: []string{x.root}, Now: x.clock,
	}
}

// register registers the project from a checkout as `project add --yes`.
func (x *h) register(entry string) *project.Registration {
	x.t.Helper()
	plan, err := x.runner().Plan(context.Background(), entry)
	if err != nil {
		x.t.Fatal(err)
	}
	if err := project.Save(x.state, plan.Reg); err != nil {
		x.t.Fatal(err)
	}
	return plan.Reg
}

// command is one CLI command run from cwd: a fresh session whose project
// cycles run in-process, synchronously (a stand-in for the detached child).
// It returns the header and status lines, or the query's error.
type cmdResult struct {
	header string
	status []string
	err    error
	cycles int
}

func (x *h) command(cwd string) cmdResult {
	x.t.Helper()
	ctx := context.Background()
	var res cmdResult
	sess := &session.Session{Store: x.s, DataDir: filepath.Dir(x.db), DB: x.db, Cwd: cwd, StateDir: x.state, Now: x.clock}
	sess.ProjectRebuild = func(name string) {
		res.cycles++
		x.runner().Cycle(ctx, name, false)
	}
	q, rep, err := sess.Query(ctx)
	if err != nil {
		res.err = err
		return res
	}
	res.header, res.status = header(x.t, q), rep.Lines()
	return res
}

func header(t *testing.T, q *query.Q) string {
	d, err := q.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d.Header[0]
}

// mapped returns the newest main snapshot's repo revisions and map id.
func (x *h) mapped() (map[string]string, string) {
	x.t.Helper()
	si, ok, err := x.s.Newest(context.Background(), "shop", "main")
	if err != nil || !ok {
		return nil, ""
	}
	repos, _ := x.s.Repos(context.Background(), si.ID)
	out := map[string]string{}
	for _, r := range repos {
		out[r.Name] = r.Revision
	}
	return out, si.MapID
}

func (x *h) snapshots() int {
	all, _ := x.s.Snapshots(context.Background())
	return len(all)
}

func (x *h) state0() *project.State {
	st, err := project.LoadState(x.state, "shop")
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func execGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
