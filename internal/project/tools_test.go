package project_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
)

// realMise returns the real mise and the data directory holding an
// installed go@1.27, or skips. Nothing is installed by these tests.
func realMise(t *testing.T) (bin, dataDir string) {
	t.Helper()
	bin, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise not on PATH")
	}
	cmd := exec.Command(bin, "where", "go@1.27")
	cmd.Env = append(os.Environ(), "MISE_NO_CONFIG=1")
	out, err := cmd.Output()
	if err != nil {
		t.Skip("go@1.27 not installed in mise")
	}
	where := strings.TrimSpace(string(out))
	i := strings.Index(where, string(filepath.Separator)+"installs"+string(filepath.Separator))
	if i < 0 {
		t.Skip("unexpected mise layout " + where)
	}
	return bin, where[:i]
}

func runIn(t *testing.T, env []string, dir string, argv ...string) (string, error) {
	t.Helper()
	p := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, d := range filepath.SplitList(v) {
				if _, err := os.Stat(filepath.Join(d, argv[0])); err == nil {
					p = filepath.Join(d, argv[0])
					break
				}
			}
		}
	}
	if p == "" {
		t.Fatalf("%s not on the tools' PATH", argv[0])
	}
	cmd := exec.Command(p, argv[1:]...)
	cmd.Env, cmd.Dir = env, dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// With the real mise, a decoy go first on PATH, and the scratch, the repo
// and mise's own config all under $HOME: the go that runs is the named one
// from mise's install, no repo, home or global mise config is read (their
// [env] markers stay unset), nothing is installed, and go.mod's toolchain
// line doesn't make Go fetch another toolchain.
func TestMiseRunsOnlyNamedTools(t *testing.T) {
	mise, data := realMise(t)
	home := t.TempDir()
	decoy := filepath.Join(home, "decoy")
	writeFiles(t, decoy, map[string]string{"go": "#!/bin/sh\necho DECOY\nexit 7\n"})
	os.Chmod(filepath.Join(decoy, "go"), 0o755)
	writeFiles(t, home, map[string]string{
		".config/mise/config.toml": "[env]\nCARTO_MARKER_GLOBAL = \"global\"\n[tools]\nnode = \"0.0.1\"\n",
		"mise.toml":                "[env]\nCARTO_MARKER_HOME = \"home\"\n",
	})
	repo := filepath.Join(home, "state", "build", "repos", "app")
	writeFiles(t, repo, map[string]string{
		"mise.toml": "[env]\nCARTO_MARKER_REPO = \"repo\"\n[tools]\ngo = \"1.27\"\nnode = \"0.0.2\"\n",
		"go.mod":    "module example.com/app\n\ngo 1.27\n\ntoolchain go1.99.0\n",
		"main.go":   "package main\n\nfunc main() {}\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("MISE_DATA_DIR", data)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", strings.Join([]string{decoy, filepath.Dir(mise), "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GOFLAGS", "-mod=mod")

	pins, err := project.Pins(func(p string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(repo, p))
		if os.IsNotExist(err) {
			return nil, project.ErrNoFile
		}
		return b, err
	})
	if err != nil || pins["go"] != "1.27" {
		t.Fatalf("pins %v %v", pins, err)
	}
	m := &project.Mise{Dir: filepath.Join(home, "state")}
	os.MkdirAll(m.Dir, 0o755)
	env, err := m.Env(context.Background(), []project.Tool{{Name: "go", Version: pins["go"]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Installs) != 0 {
		t.Errorf("installed %v", m.Installs)
	}
	out, err := runIn(t, env, repo, "go", "env", "GOVERSION", "GOROOT")
	if err != nil || !strings.HasPrefix(out, "go1.27") || !strings.HasPrefix(strings.Split(out, "\n")[1], data) {
		t.Fatalf("go env: %v\n%s", err, out)
	}
	// go.mod asks for go1.99.0: with GOTOOLCHAIN=local, Go builds with the
	// named go and fetches nothing.
	if out, err := runIn(t, env, repo, "go", "list", "-m"); err != nil || out != "example.com/app" {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	out, _ = runIn(t, env, repo, "sh", "-c", `echo "[$CARTO_MARKER_REPO$CARTO_MARKER_HOME$CARTO_MARKER_GLOBAL]"`)
	if out != "[]" {
		t.Errorf("a mise config was read: %s", out)
	}
}

// A missing pin fails, a version mise can't provide fails, and only the
// named tools are asked for (fake mise).
func TestMiseFailsClosed(t *testing.T) {
	if _, err := project.ExtractorTools("go", map[string]string{"node": "26"}); err == nil || !strings.Contains(err.Error(), "no go version: the repo must pin it") {
		t.Errorf("missing pin: %v", err)
	}
	x := newH(t)
	m := &project.Mise{Bin: filepath.Join(x.dir, "mise"), Dir: x.dir}
	if _, err := m.Env(context.Background(), []project.Tool{{Name: "go", Version: "0.0.0-none"}}); err == nil || !strings.Contains(err.Error(), "go@0.0.0-none: mise can't provide it") {
		t.Errorf("unprovidable: %v", err)
	}
	// Installable: installed once, then found.
	writeFiles(t, filepath.Join(x.tools, "available", "node_26", "bin"), map[string]string{"node": "#!/bin/sh\n"})
	os.Chmod(filepath.Join(x.tools, "available", "node_26", "bin", "node"), 0o755)
	if _, err := m.Env(context.Background(), []project.Tool{{Name: "node", Version: "26"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.Installs) != 1 || m.Installs[0] != "node@26" {
		t.Errorf("installs %v", m.Installs)
	}
	// A tool whose program isn't in its install fails.
	os.MkdirAll(filepath.Join(x.tools, "rust_1.99.1", "bin"), 0o755)
	if _, err := m.Env(context.Background(), []project.Tool{{Name: "rust", Version: "1.99.1"}}); err == nil || !strings.Contains(err.Error(), "rust@1.99.1: cargo") {
		t.Errorf("empty install: %v", err)
	}
	// A whole build asks mise only about the tools its extractors name, not
	// the repo's other pins.
	_, sales, _ := x.world()
	x.push("sales", map[string]string{".tool-versions": elixirPins + "nodejs 0.0.3\ngo 0.0.4\n"})
	x.register(sales)
	os.Remove(x.miseLog)
	if _, err := x.runner().Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(x.miseLog)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		for _, f := range strings.Fields(line)[1:] {
			if f != "-J" && f != "--installed" && f != "erlang@28.3.1" && f != "elixir@1.19.5-otp-28" {
				t.Errorf("mise asked about %s: %s", f, line)
			}
		}
	}
}

func TestPinsReadTablesArraysAndToolVersions(t *testing.T) {
	files := map[string]string{
		"mise.toml":      "[tools]\nrust = { version = \"1.99.1\", components = \"rust-analyzer\" }\nnode = [\"26\", \"24\"]\n",
		".mise.toml":     "[tools]\ngo = \"1.26.11\"\nrust = \"0.0.0\"\n",
		".tool-versions": "erlang 28.3.1 # comment\nelixir 1.19.5-otp-28\nnodejs 22\n",
	}
	pins, err := project.ResolvePins(func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}
		return nil, project.ErrNoFile
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"rust": "1.99.1", "node": "26", "go": "1.26.11", "erlang": "28.3.1", "elixir": "1.19.5-otp-28"}
	for k, v := range want {
		if pins[k] != v {
			t.Errorf("%s = %q, want %q (%v)", k, pins[k], v, pins)
		}
	}
}
