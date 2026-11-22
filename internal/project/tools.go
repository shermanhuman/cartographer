package project

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Indexer versions pinned by this release.
var (
	ScipGo       = Tool{Name: "github:scip-code/scip-go", Version: "0.2.9"}
	RustAnalyzer = Tool{Name: "aqua:rust-lang/rust-analyzer", Version: "2026-11-16"}
	// Node runs the bundled JavaScript inventory plugin, which runs none of
	// the repo's code, so its version is cartographer's, not the repo's
	// (repos using it often pin no Node at all).
	Node = Tool{Name: "node", Version: "26.12.1"}
	// Postgres is the throwaway cluster of a `postgres = "managed"` schema
	// step (mise's postgres, the conda:postgresql backend); one version
	// gives one schema whatever the machine.
	Postgres = Tool{Name: "postgres", Version: "18.7"}
)

// RustTarget is the target added to the Rust toolchain (scip.RustTarget).
const RustTarget = "x86_64-unknown-linux-gnu"

// Tool is one tool as mise names it, at one version.
type Tool struct{ Name, Version string }

// Spec is name@version.
func (t Tool) Spec() string { return t.Name + "@" + t.Version }

// Bin is the program that must resolve inside the tool's install.
func (t Tool) Bin() string {
	switch t.Name {
	case "rust":
		return "cargo"
	case "erlang":
		return "erl"
	}
	if i := strings.LastIndexAny(t.Name, ":/"); i >= 0 {
		return t.Name[i+1:]
	}
	return t.Name
}

// pinFiles are a repo's tool pin files, in the order they are consulted.
var pinFiles = []string{"mise.toml", ".mise.toml", ".tool-versions"}

// Pins reads the language versions (Languages) a repo pins in its own
// mise.toml, .mise.toml or .tool-versions (the first that names a language
// wins). read returns ErrNoFile for a missing file. A table pin gives its
// version; an array its first entry.
func Pins(read func(path string) ([]byte, error)) (map[string]string, error) {
	pins := map[string]string{}
	set := func(lang, v string) {
		if lang == "nodejs" {
			lang = "node"
		}
		if _, ok := pins[lang]; !ok && slices.Contains(Languages, lang) && v != "" {
			pins[lang] = v
		}
	}
	for _, f := range pinFiles {
		b, err := read(f)
		if errors.Is(err, ErrNoFile) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if f == ".tool-versions" {
			sc := bufio.NewScanner(bytes.NewReader(b))
			for sc.Scan() {
				line, _, _ := strings.Cut(sc.Text(), "#")
				if fs := strings.Fields(line); len(fs) >= 2 {
					set(fs[0], fs[1])
				}
			}
			continue
		}
		var doc struct {
			Tools map[string]any `toml:"tools"`
		}
		if _, err := toml.Decode(string(b), &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for k, v := range doc.Tools {
			set(k, pinVersion(v))
		}
	}
	return pins, nil
}

func pinVersion(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			return pinVersion(v[0])
		}
	case map[string]any:
		return pinVersion(v["version"])
	}
	return ""
}

// ExtractorTools are the tools an extractor runs with, the indexer first
// (its binary wins over a same-named proxy of the language's toolchain).
// pins are the repo's languages (ResolvePins).
func ExtractorTools(extractor string, pins map[string]string) ([]Tool, error) {
	need := func(langs ...string) ([]Tool, error) {
		var out []Tool
		for _, l := range langs {
			v := pins[l]
			if v == "" {
				return nil, fmt.Errorf("no %s version: the repo must pin it in its mise.toml, .mise.toml or .tool-versions", l)
			}
			out = append(out, Tool{Name: l, Version: v})
		}
		return out, nil
	}
	switch extractor {
	case "go":
		t, err := need("go")
		return append([]Tool{ScipGo}, t...), err
	case "rust":
		t, err := need("rust")
		return append([]Tool{RustAnalyzer}, t...), err
	case "elixir", "schema":
		return need("erlang", "elixir") // elixir installs on erlang
	case "javascript":
		return []Tool{Node}, nil
	}
	return nil, nil // in-process
}

// SchemaTools are the tools the schema step runs with: the repo's Erlang
// and Elixir, and Postgres when it is managed (psql for the capture).
func SchemaTools(sc *SchemaSettings, pins map[string]string) ([]Tool, error) {
	t, err := ExtractorTools("schema", pins)
	if err == nil && sc.Managed() {
		t = append(t, Postgres)
	}
	return t, err
}

// Mise provides named tools through mise, and nothing else: no repo,
// global or system mise config is read (MISE_NO_CONFIG), Go never switches
// toolchains (GOTOOLCHAIN=local), each tool is installed when missing, and
// each tool's program must resolve inside its own install before anything
// runs. `mise exec` itself isn't used to run commands: it keeps PATH
// entries that already precede its own (an activated shell's), so an
// ambient program could win.
type Mise struct {
	// Bin is the mise executable (default: mise on PATH).
	Bin string
	// Dir is where mise runs (any directory without config).
	Dir string
	Log io.Writer
	// InstallTimeout bounds each `mise install` (default
	// DefaultInstallTimeout).
	InstallTimeout time.Duration

	mu       sync.Mutex
	paths    map[string][]string // spec -> bin paths
	Installs []string            // specs installed by this Mise
}

// DefaultInstallTimeout bounds one tool's install (Erlang builds from
// source on Linux). It is its own deadline: installs run before a
// project's build (installTools), so they don't count against the build's
// limit (rebuild.Timeout, 15 minutes), and a stalled one can't hold the
// project's lock for longer than this. An install the build's own tools
// step still makes is bounded by both.
const DefaultInstallTimeout = 30 * time.Minute

func (m *Mise) mise(ctx context.Context, args ...string) (string, error) {
	return m.miseWith(ctx, nil, args...)
}

// miseWith runs mise with pathFirst ahead of PATH: an install whose own
// check runs another tool (Elixir's runs erl) finds that tool there.
func (m *Mise) miseWith(ctx context.Context, pathFirst []string, args ...string) (string, error) {
	bin := m.Bin
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("mise"); err != nil {
			return "", errors.New("mise is not on PATH: project builds get every tool from mise (https://mise.jdx.dev)")
		}
		m.Bin = bin
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = m.Dir
	cmd.Env = append(miseFree(os.Environ()), "MISE_NO_CONFIG=1", "MISE_YES=1")
	if len(pathFirst) > 0 {
		cmd.Env = append(cmd.Env, "PATH="+strings.Join(pathFirst, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	// Its own process group: a cancelled install ends the compilers and
	// downloads it started too, and WaitDelay closes the pipes they hold.
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := workspace.RunGroup(cmd); err != nil {
		msg := workspace.Tail(strings.TrimSpace(errb.String()))
		if ctx.Err() != nil {
			msg = strings.TrimSpace(msg + " (" + ctx.Err().Error() + ")")
		}
		return "", fmt.Errorf("mise %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

// miseFree drops environment that would make mise or Go pick tools or
// config by themselves.
func miseFree(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "MISE_NO_CONFIG", k == "GOTOOLCHAIN", k == "MISE_ENV", k == "MISE_YES":
			continue
		case strings.HasPrefix(k, "MISE_") && strings.HasSuffix(k, "_VERSION"):
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ensure installs t when mise doesn't have it and returns its bin paths.
// before holds the bin paths of tools already ensured in the same set, put
// ahead of PATH for the install.
func (m *Mise) ensure(ctx context.Context, t Tool, before ...string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.paths[t.Spec()]; ok {
		return p, nil
	}
	if _, err := m.mise(ctx, "where", t.Spec()); err != nil {
		if m.Log != nil {
			fmt.Fprintf(m.Log, "mise: installing %s\n", t.Spec())
		}
		timeout := m.InstallTimeout
		if timeout == 0 {
			timeout = DefaultInstallTimeout
		}
		ictx, cancel := context.WithTimeout(ctx, timeout)
		_, err := m.miseWith(ictx, before, "install", t.Spec())
		cancel()
		if err != nil {
			if errors.Is(ictx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				err = fmt.Errorf("%w (the install stopped after its %s limit)", err, timeout)
			}
			return nil, fmt.Errorf("%s: mise can't provide it: %w", t.Spec(), err)
		}
		m.Installs = append(m.Installs, t.Spec())
		if _, err := m.mise(ctx, "where", t.Spec()); err != nil {
			return nil, fmt.Errorf("%s: still not installed after mise install: %w", t.Spec(), err)
		}
	}
	out, err := m.mise(ctx, "bin-paths", t.Spec())
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s: mise lists no bin paths", t.Spec())
	}
	if m.paths == nil {
		m.paths = map[string][]string{}
	}
	m.paths[t.Spec()] = paths
	return paths, nil
}

// Env returns the environment a command needs to run with exactly tools:
// the process environment without credentials, mise's environment for the
// tools (GOROOT, RUSTUP_TOOLCHAIN, …), PATH starting with the tools' bin
// paths in order, MISE_NO_CONFIG=1 and GOTOOLCHAIN=local. It fails unless
// each tool's program resolves inside its own bin paths.
func (m *Mise) Env(ctx context.Context, tools []Tool) ([]string, error) {
	var bins []string
	owned := map[string][]string{}
	for _, t := range installOrder(tools) {
		p, err := m.ensure(ctx, t, bins...)
		if err != nil {
			return nil, err
		}
		bins = append(bins, p...)
		owned[t.Spec()] = p
	}
	extra := map[string]string{}
	if len(tools) > 0 {
		args := []string{"env", "-J"}
		for _, t := range tools {
			args = append(args, t.Spec())
		}
		out, err := m.mise(ctx, args...)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(out), &extra); err != nil {
			return nil, fmt.Errorf("mise env: %w", err)
		}
	}
	delete(extra, "PATH")
	var env []string
	pathVar := ""
	for _, kv := range workspace.ChildEnv(miseFree(os.Environ())) {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" {
			pathVar = v
			continue
		}
		if _, ok := extra[k]; !ok {
			env = append(env, kv)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	path := strings.Join(append(bins, ambientPath(pathVar)...), string(os.PathListSeparator))
	env = append(env, "PATH="+path, "MISE_NO_CONFIG=1", "GOTOOLCHAIN=local")
	for _, t := range tools {
		if err := resolvesInside(path, t.Bin(), owned[t.Spec()]); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Spec(), err)
		}
	}
	return env, nil
}

// Install installs each tool mise doesn't have (in installOrder, each
// install seeing the ones before it on PATH).
func (m *Mise) Install(ctx context.Context, tools []Tool) error {
	var bins []string
	for _, t := range installOrder(tools) {
		p, err := m.ensure(ctx, t, bins...)
		if err != nil {
			return err
		}
		bins = append(bins, p...)
	}
	return nil
}

// ResolveInstalled returns the tools at the concrete versions mise runs
// (go@1.25 -> go@1.25.3), in the same order, installing nothing: the
// version a prefix pin means depends on what is installed, so the inputs
// digest and the manifest record the resolved one. A tool mise doesn't
// have keeps its version as given.
func (m *Mise) ResolveInstalled(ctx context.Context, tools []Tool) ([]Tool, error) {
	got := map[string]string{}
	for _, t := range tools {
		if _, err := m.mise(ctx, "where", t.Spec()); err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			got[t.Spec()] = t.Version
			continue
		}
		out, err := m.mise(ctx, "latest", "--installed", t.Spec())
		if err != nil {
			return nil, err
		}
		v := strings.TrimSpace(out)
		if v == "" || strings.ContainsAny(v, " \n") {
			return nil, fmt.Errorf("%s: mise names no installed version (%q)", t.Spec(), v)
		}
		got[t.Spec()] = v
	}
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = Tool{Name: t.Name, Version: got[t.Spec()]}
	}
	return out, nil
}

// BinPaths installs t when mise doesn't have it and returns its bin paths,
// once each of bins resolves inside them.
func (m *Mise) BinPaths(ctx context.Context, t Tool, bins ...string) ([]string, error) {
	paths, err := m.ensure(ctx, t)
	if err != nil {
		return nil, err
	}
	path := strings.Join(paths, string(os.PathListSeparator))
	for _, b := range bins {
		if err := resolvesInside(path, b, paths); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Spec(), err)
		}
	}
	return paths, nil
}

// ambientPath is the process PATH without mise's shims and installs: a
// tool nobody named must not resolve.
func ambientPath(p string) []string {
	var out []string
	for _, d := range filepath.SplitList(p) {
		s := filepath.ToSlash(d)
		if strings.Contains(s, "/mise/shims") || strings.Contains(s, "/mise/installs/") {
			continue
		}
		out = append(out, d)
	}
	return out
}

// resolvesInside checks that bin, looked up on path, is a file inside one
// of dirs (symlinks resolved).
func resolvesInside(path, bin string, dirs []string) error {
	found := ""
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			found = p
			break
		}
	}
	if found == "" {
		return fmt.Errorf("%s isn't in its mise install", bin)
	}
	for _, d := range dirs {
		if samePath(filepath.Dir(found), d) {
			return nil
		}
	}
	return fmt.Errorf("%s resolves to %s, outside its mise install (%s)", bin, found, strings.Join(dirs, ", "))
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// RustTargetAdd adds RustTarget to the toolchain env selects.
func RustTargetAdd(ctx context.Context, env []string, dir string) error {
	cmd := exec.CommandContext(ctx, "rustup", "target", "add", RustTarget)
	cmd.Env, cmd.Dir = env, dir
	// rustup resolves through PATH from env, not the process PATH.
	if p, err := lookIn(env, "rustup"); err == nil {
		cmd.Path = p
	} else {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rustup target add %s: %v: %s", RustTarget, err, workspace.Tail(string(out)))
	}
	return nil
}

func lookIn(env []string, bin string) (string, error) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, d := range filepath.SplitList(v) {
				p := filepath.Join(d, bin)
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s not found on the tools' PATH", bin)
}

// installOrder puts the tools another tool's install needs first (Erlang
// before Elixir), keeping the rest in their order.
func installOrder(tools []Tool) []Tool {
	first := map[string]bool{"erlang": true}
	var a, b []Tool
	for _, t := range tools {
		if first[t.Name] {
			a = append(a, t)
		} else {
			b = append(b, t)
		}
	}
	return append(a, b...)
}
