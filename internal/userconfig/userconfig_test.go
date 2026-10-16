package userconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func use(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cartographer", "config.toml")
	if body != "" {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CARTOGRAPHER_CONFIG", p)
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadMissingIsEmpty(t *testing.T) {
	use(t, "")
	c, err := Load()
	if err != nil || len(c.Workspaces) != 0 || len(c.Checkouts) != 0 {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestAddWorkspaceCreatesTheFile(t *testing.T) {
	p := use(t, "")
	if err := AddWorkspace("acme/map"); err != nil {
		t.Fatal(err)
	}
	if err := AddWorkspace("acme/map"); err != nil { // idempotent
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || !slices.Equal(c.Workspaces, []string{"acme/map"}) {
		t.Fatalf("Load = %+v, %v\n%s", c, err, read(t, p))
	}
}

func TestAddWorkspaceKeepsCommentsAndCheckouts(t *testing.T) {
	const orig = `# my cartographer config
workspaces = [
  "acme/map", # the main map
  # "acme/old", retired
]

# checkouts outside the sibling layout
[checkouts]
shop-app = "~/src/shop-app" # laptop
`
	p := use(t, orig)
	if err := AddWorkspace("acme/other"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"# my cartographer config", "# the main map", `# "acme/old", retired`, "# checkouts outside the sibling layout", "# laptop"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	c, err := Load()
	if err != nil || !slices.Equal(c.Workspaces, []string{"acme/map", "acme/other"}) || c.Checkouts["shop-app"] != "~/src/shop-app" {
		t.Fatalf("Load = %+v, %v\n%s", c, err, got)
	}
}

func TestAddWorkspaceToAFileWithoutTheKey(t *testing.T) {
	for _, orig := range []string{
		"# only checkouts\n[checkouts]\nshop-app = \"/src/shop-app\"\n",
		"workspaces = []\n",
		"workspaces = [\"a/b\"]",
		"workspaces = ['a/b',] # trailing comma\n",
	} {
		p := use(t, orig)
		if err := AddWorkspace("acme/map"); err != nil {
			t.Fatalf("%q: %v", orig, err)
		}
		c, err := Load()
		if err != nil || !slices.Contains(c.Workspaces, "acme/map") {
			t.Errorf("%q: Load = %+v, %v\n%s", orig, c, err, read(t, p))
		}
		if strings.HasPrefix(orig, "#") && (!strings.Contains(read(t, p), "# only checkouts") || c.Checkouts["shop-app"] != "/src/shop-app") {
			t.Errorf("%q: lost content:\n%s", orig, read(t, p))
		}
	}
}

func TestAddWorkspaceRejectsBadSources(t *testing.T) {
	use(t, "")
	for _, src := range []string{"", "acme", "acme/map/x", "../x/y", "a b/c"} {
		if err := AddWorkspace(src); err == nil {
			t.Errorf("AddWorkspace(%q) succeeded", src)
		}
	}
}

// In a multi-line array the new entry gets its own line before the closing
// bracket, so a comment stays on the entry it describes.
func TestAddWorkspaceMultiLineArrayKeepsCommentPlacement(t *testing.T) {
	p := use(t, "workspaces = [\n  \"acme/map\", # the main map\n]\n")
	if err := AddWorkspace("acme/other"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "workspaces = [\n  \"acme/map\", # the main map\n  \"acme/other\",\n]\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Same when the last entry has no trailing comma.
	p = use(t, "workspaces = [\n  \"acme/map\" # the main map\n]\n")
	if err := AddWorkspace("acme/other"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "workspaces = [\n  \"acme/map\", # the main map\n  \"acme/other\",\n]\n"; got != want {
		t.Errorf("no trailing comma, got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddLocalWorkspaceRecordsADirectory(t *testing.T) {
	const orig = `# my cartographer config
workspaces = ["acme/map"] # published

[checkouts]
shop-app = "~/src/shop-app"
`
	p := use(t, orig)
	dir := workspaceDir(t)
	if err := AddLocalWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	if err := AddLocalWorkspace(dir); err != nil { // idempotent
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"# my cartographer config", "# published", "[checkouts]"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	c, err := Load()
	if err != nil || !slices.Equal(c.LocalWorkspaces, []string{dir}) || !slices.Equal(c.Workspaces, []string{"acme/map"}) || c.Checkouts["shop-app"] != "~/src/shop-app" {
		t.Fatalf("Load = %+v, %v\n%s", c, err, got)
	}
	// A second directory joins the array; a relative one is rejected.
	other := workspaceDir(t)
	if err := AddLocalWorkspace(other); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); !slices.Equal(c.LocalWorkspaces, []string{dir, other}) {
		t.Errorf("second add: %v\n%s", c.LocalWorkspaces, read(t, p))
	}
	if err := AddLocalWorkspace("map"); err == nil {
		t.Error("a relative path was recorded")
	}
}

// auto_rebuild is a tri-state: unset means on.
func TestLoadAutoRebuild(t *testing.T) {
	use(t, "")
	if c, _ := Load(); !c.AutoRebuildOn() {
		t.Error("unset auto_rebuild must be on")
	}
	use(t, "auto_rebuild = false\n")
	if c, _ := Load(); c.AutoRebuildOn() {
		t.Error("auto_rebuild = false ignored")
	}
	use(t, "auto_rebuild = true\n")
	if c, _ := Load(); !c.AutoRebuildOn() {
		t.Error("auto_rebuild = true ignored")
	}
}

// A directory already listed in another spelling (a trailing slash) is not
// added twice.
func TestAddLocalWorkspaceDedupesSpellings(t *testing.T) {
	dir := workspaceDir(t)
	p := use(t, "local_workspaces = ["+strconv.Quote(dir+"/")+"]\n")
	if err := AddLocalWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); len(c.LocalWorkspaces) != 1 {
		t.Errorf("added twice:\n%s", read(t, p))
	}
}

// workspaceDir is a directory holding a cartographer.toml, symlinks resolved.
func workspaceDir(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(dir, "cartographer.toml"), []byte("name = \"mini\"\n"), 0o644)
	return dir
}

// A relative local_workspaces entry would trust whatever directory a query
// runs in: Load drops it with a warning naming the file to edit, keeps the
// rest of the config, and returns entries absolute (~ expanded).
func TestLoadSkipsRelativeLocalWorkspaces(t *testing.T) {
	for _, entry := range []string{".", "map", "../map", "~user/map"} {
		p := use(t, "workspaces = [\"acme/map\"]\nlocal_workspaces = ["+strconv.Quote(entry)+", \"/abs/map\"]\n[checkouts]\nshop-app = \"/src/shop-app\"\n")
		c, err := Load()
		if err != nil || !slices.Equal(c.LocalWorkspaces, []string{"/abs/map"}) || len(c.Workspaces) != 1 || c.Checkouts["shop-app"] != "/src/shop-app" {
			t.Fatalf("%q: Load = %+v, %v", entry, c, err)
		}
		if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "edit "+p) || !strings.Contains(c.Warnings[0], strconv.Quote(entry)) {
			t.Errorf("%q: warnings %q", entry, c.Warnings)
		}
	}
	// The other commands that load the config keep working.
	if err := AddWorkspace("acme/other"); err != nil {
		t.Errorf("AddWorkspace with a bad entry: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	use(t, "local_workspaces = [\"~/src/map/\", \"/abs/map\"]\n")
	c, err := Load()
	if err != nil || !slices.Equal(c.LocalWorkspaces, []string{filepath.Join(home, "src", "map"), "/abs/map"}) || len(c.Warnings) != 0 {
		t.Errorf("Load = %v, %v", c, err)
	}
}

// workspace add records a digest of cartographer.toml: trust holds while
// the file is unchanged, an edit (a pull of the map repo, a swapped
// directory) needs a fresh add, and a directory never added is unknown.
func TestTrustFollowsTheWorkspaceFile(t *testing.T) {
	use(t, "")
	dir := t.TempDir()
	toml := filepath.Join(dir, "cartographer.toml")
	os.WriteFile(toml, []byte("name = \"mini\"\n"), 0o644)
	if got := Trust(dir); got != TrustUnknown {
		t.Errorf("before add: %v", got)
	}
	if err := AddLocalWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	if got := Trust(dir); got != Trusted {
		t.Errorf("after add: %v", got)
	}
	os.WriteFile(toml, []byte("name = \"mini\"\n[plugins.x]\ncommand = [\"evil\"]\n"), 0o644)
	if got := Trust(dir); got != TrustChanged {
		t.Errorf("after an edit: %v", got)
	}
	if err := AddLocalWorkspace(dir); err != nil { // re-adding refreshes it
		t.Fatal(err)
	}
	if got := Trust(dir); got != Trusted {
		t.Errorf("after re-adding: %v", got)
	}
	if c, _ := Load(); len(c.LocalWorkspaces) != 1 {
		t.Errorf("re-adding duplicated the entry: %v", c.LocalWorkspaces)
	}
}

// A symlink to a registered workspace is the same workspace; a retargeted
// symlink is not trusted.
func TestAddLocalWorkspaceResolvesSymlinks(t *testing.T) {
	use(t, "")
	real := t.TempDir()
	os.WriteFile(filepath.Join(real, "cartographer.toml"), []byte("name = \"mini\"\n"), 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if err := AddLocalWorkspace(real); err != nil {
		t.Fatal(err)
	}
	if err := AddLocalWorkspace(link); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); len(c.LocalWorkspaces) != 1 {
		t.Errorf("symlink recorded twice: %v", c.LocalWorkspaces)
	}
	if got := Trust(link); got != Trusted {
		t.Errorf("via the link: %v", got)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "cartographer.toml"), []byte("name = \"mini\"\n"), 0o644)
	os.Remove(link)
	os.Symlink(other, link)
	if got := Trust(link); got != TrustUnknown {
		t.Errorf("retargeted link: %v", got)
	}
}
