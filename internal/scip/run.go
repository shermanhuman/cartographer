package scip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// RustTarget is the target rust-analyzer resolves cfg for, the same on every
// machine.
const RustTarget = "x86_64-unknown-linux-gnu"

// GoEnv is the fixed build environment scip-go runs with, so every machine
// sees the same files: build tags and cgo decide which files exist.
var GoEnv = []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}

// RunOptions configure an indexer run.
type RunOptions struct {
	Options
	// RustBuildScripts runs build scripts and proc macros (rust-analyzer
	// needs them to see generated code). Off, both are skipped.
	RustBuildScripts bool
	// Env is the base environment for the indexer (default os.Environ());
	// indexers are found on its PATH.
	Env []string
	// Log receives the indexers' output (default os.Stderr).
	Log io.Writer
}

// Extract indexes a repository and converts the index.
func Extract(ctx context.Context, opt RunOptions) (*facts.Doc, error) {
	tmp, err := os.MkdirTemp("", "cartographer-scip-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	idx, err := RunIndexer(ctx, opt, tmp)
	if err != nil {
		return nil, err
	}
	return Convert(opt.Options, idx)
}

// RunIndexer runs the language's indexer on opt.Root, writing indexes under
// dir, and reads them back.
func RunIndexer(ctx context.Context, opt RunOptions, dir string) ([]Index, error) {
	if opt.Log == nil {
		opt.Log = os.Stderr
	}
	if opt.Env == nil {
		opt.Env = os.Environ()
	}
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}
	switch opt.Lang {
	case Go:
		mods, err := GoModules(root)
		if err != nil {
			return nil, err
		}
		if len(mods) == 0 {
			return nil, fmt.Errorf("scip: no go.mod under %s", root)
		}
		env := setEnv(opt.Env, GoEnv...)
		var out []Index
		for i, m := range mods {
			file := filepath.Join(dir, fmt.Sprintf("go-%d.scip", i))
			// A fixed module version keeps the index independent of the
			// revision; versions are dropped from ids anyway.
			args := []string{"index", "--module-version=0", "--repository-remote=" + opt.Repo, "-o", file, "./..."}
			if err := run(ctx, env, filepath.Join(root, filepath.FromSlash(m)), opt.Log, "scip-go", args...); err != nil {
				return nil, err
			}
			idx, err := ReadIndex(file)
			if err != nil {
				return nil, err
			}
			out = append(out, Index{Dir: m, Index: idx})
		}
		return out, nil
	case Rust:
		if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err != nil {
			return nil, fmt.Errorf("scip: no Cargo.toml at %s", root)
		}
		if err := run(ctx, opt.Env, root, opt.Log, "cargo", "fetch"); err != nil {
			return nil, err
		}
		cfg := filepath.Join(dir, "rust-analyzer.json")
		if err := os.WriteFile(cfg, RustConfig(opt.RustBuildScripts), 0o644); err != nil {
			return nil, err
		}
		file := filepath.Join(dir, "rust.scip")
		if err := run(ctx, opt.Env, root, opt.Log, "rust-analyzer", "scip", ".", "--output", file, "--config-path", cfg); err != nil {
			return nil, err
		}
		idx, err := ReadIndex(file)
		if err != nil {
			return nil, err
		}
		return []Index{{Dir: ".", Index: idx}}, nil
	}
	return nil, fmt.Errorf("scip: language %q: want go or rust", opt.Lang)
}

// RustConfig is rust-analyzer's configuration: the target is pinned. With
// buildScripts false, build scripts and proc macros don't run. rust-analyzer
// scip ignores cargo.buildScripts.enable and procMacro.enable (it always
// loads build data), so an override command that prints nothing stands in
// for them: no build script output and no proc-macro dylibs.
func RustConfig(buildScripts bool) []byte {
	cargo := map[string]any{"target": RustTarget}
	cfg := map[string]any{"cargo": cargo}
	if !buildScripts {
		cargo["buildScripts"] = map[string]any{"enable": false, "overrideCommand": []string{"true"}}
		cfg["procMacro"] = map[string]any{"enable": false}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return b
}

// GoModules lists the directories under root holding a go.mod, relative to
// root with "/" separators. vendor, testdata, node_modules and hidden
// directories are skipped.
func GoModules(root string) ([]string, error) {
	var mods []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "vendor" || name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			mods = append(mods, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(mods)
	return mods, err
}

// setEnv returns env with each KEY=VALUE in kv replacing any earlier KEY.
func setEnv(env []string, kv ...string) []string {
	out := make([]string, 0, len(env)+len(kv))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !slices.ContainsFunc(kv, func(s string) bool { return strings.HasPrefix(s, k+"=") }) {
			out = append(out, e)
		}
	}
	return append(out, kv...)
}

// lookPath finds a program on the PATH in env.
func lookPath(env []string, name string) (string, error) {
	pathVar := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			pathVar = v
		}
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("scip: %s not found on PATH", name)
}

func run(ctx context.Context, env []string, dir string, log io.Writer, name string, args ...string) error {
	bin, err := lookPath(env, name)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = log // keep stdout for the facts document
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scip: %s %s in %s: %w", name, strings.Join(args, " "), dir, err)
	}
	return nil
}
