package project_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
)

func TestParseSettingsIsStrict(t *testing.T) {
	ok := `project = "shop"
anchor = "acme/admin"
extract = ["go", "kubernetes"]
docs = false
source_roots = ["lib"]
images = ["shop-web"]
contexts_depth = 3
[rust]
build_scripts = false
`
	s, err := project.ParseSettings([]byte(ok), "acme/sales")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Docs.None || s.Rust.BuildScriptsOn() || s.Anchor != "acme/admin" || s.ContextsDepth != 3 {
		t.Errorf("%+v", s)
	}
	if s, err := project.ParseSettings([]byte("project = \"shop\"\ndocs = []\n"), "acme/a"); err != nil || s.Docs.None || len(s.Docs.Patterns) != 0 {
		t.Errorf("docs = [] is every doc: %+v %v", s, err)
	}
	// The anchor naming itself is the anchor.
	if s, err := project.ParseSettings([]byte("project = \"shop\"\nanchor = \"Acme/Admin\"\nmembers = [{repo = \"acme/sales\"}]\n"), "acme/admin"); err != nil || s.Anchor != "" {
		t.Errorf("self anchor: %+v %v", s, err)
	}
	for _, bad := range []struct{ toml, want string }{
		{`project = "shop"` + "\ntools = { go = \"1.26\" }\n", "unknown keys [tools"},
		{`project = "shop"` + "\ncolour = 1\n", "unknown keys [colour]"},
		{`anchor = "acme/admin"`, "project is required"},
		{`project = "shop"` + "\nextract = [\"python\"]\n", `unknown extractor "python"`},
		{`project = "shop"` + "\ndocs = true\n", "docs: want a list"},
		{`project = "shop"` + "\nanchor = \"acme/admin\"\nmembers = [{repo = \"acme/x\"}]\n", "members belong in the anchor"},
		{`project = "shop"` + "\nmembers = [{repo = \"acme/x\"}, {repo = \"other/x\"}]\n", "listed twice"},
		{`project = "shop"` + "\n[schema]\nprepare = \"x\"\n", "schema.capture is required"},
		{`project = "shop"` + "\nsource_roots = [\"../up\"]\n", "source_roots"},
	} {
		if _, err := project.ParseSettings([]byte(bad.toml), "acme/sales"); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: want %q, got %v", bad.toml, bad.want, err)
		}
	}
}

// project init writes a starter repo.toml, refuses to overwrite one, and
// prints the commands that commit it on a new branch (committing nothing).
func TestProjectInitWritesStarterSettings(t *testing.T) {
	x := newH(t)
	co := filepath.Join(x.root, "fresh")
	os.MkdirAll(co, 0o755)
	git(t, co, "init", "-q")
	git(t, co, "remote", "add", "origin", "git@github.com:acme/fresh.git")
	writeFiles(t, co, map[string]string{"go.mod": "module x\n", "k8s/a.yaml": "kind: Service\n"})
	file, cmds, err := project.Init(context.Background(), co, "shop", "acme/admin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	s, err := project.ParseSettings(b, "acme/fresh")
	if err != nil {
		t.Fatalf("the starter doesn't parse: %v\n%s", err, b)
	}
	if s.Project != "shop" || s.Anchor != "acme/admin" || strings.Join(s.Extract, ",") != "go,kubernetes" || s.Docs.None {
		t.Errorf("%+v", s)
	}
	want := []string{
		"git -C " + co + " switch -c cartographer-settings",
		"git -C " + co + " add .cartographer",
		"git -C " + co + ` commit -m "Add cartographer settings (.cartographer/repo.toml)"`,
	}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s", strings.Join(cmds, "\n"))
	}
	if out := git(t, co, "status", "--porcelain"); !strings.Contains(out, "?? ") {
		t.Errorf("expected untracked, uncommitted files: %q", out)
	}
	if _, err := git2(co, "rev-parse", "HEAD"); err == nil {
		t.Error("init committed")
	}
	if _, _, err := project.Init(context.Background(), co, "shop", "acme/admin"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrote: %v", err)
	}
	// The anchor itself gets a member list.
	co2 := filepath.Join(x.root, "anchor")
	os.MkdirAll(co2, 0o755)
	git(t, co2, "init", "-q")
	git(t, co2, "remote", "add", "origin", "https://github.com/acme/anchor")
	file, _, err = project.Init(context.Background(), co2, "shop", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if s, err := project.ParseSettings(b, "acme/anchor"); err != nil || len(s.Members) != 1 || s.Members[0].Repo != "acme/anchor" {
		t.Errorf("anchor starter: %+v %v\n%s", s, err, b)
	}
}

func git2(dir string, args ...string) (string, error) {
	out, err := execGit(dir, args...)
	return out, err
}

func TestIdentifyKeepsTheRemotesCase(t *testing.T) {
	x := newH(t)
	for _, u := range []string{"git@github.com:Acme-Labs/Shop-App.git", "https://github.com/Acme-Labs/Shop-App", "ssh://git@github.com/Acme-Labs/Shop-App.git/"} {
		co := t.TempDir()
		git(t, co, "init", "-q")
		git(t, co, "remote", "add", "origin", u)
		if got := project.Identify(context.Background(), co); got != "Acme-Labs/Shop-App" {
			t.Errorf("%s: %q", u, got)
		}
	}
	_ = x
}
