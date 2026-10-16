package userconfig

import (
	"os"
	"path/filepath"
	"slices"
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
	dir := filepath.Join(t.TempDir(), "map")
	os.MkdirAll(dir, 0o755)
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
	other := filepath.Join(t.TempDir(), "other")
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
