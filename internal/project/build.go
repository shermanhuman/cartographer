package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/extract"
	"github.com/shermanhuman/cartographer/internal/facts"
	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Input is one member as a build maps it: its frozen commit (or, for a
// --head build, its working copy) with the settings and tool pins read from
// there.
type Input struct {
	Member
	Commit   string
	Head     bool // map the working copy at Path, uncommitted files included
	Settings *Settings
	Pins     map[string]string
}

// StepError is a build that stopped at one repo's step.
type StepError struct {
	Repo   string // owner/name or name ("" project-wide)
	Commit string
	Step   string // checkout, fetch, settings, tools, archive, schema, extract <name>, assemble
	Err    error
}

func (e *StepError) Error() string {
	where := e.Repo
	if e.Commit != "" {
		where += " at " + short(e.Commit)
	}
	if where == "" {
		return e.Step + ": " + e.Err.Error()
	}
	return where + ": " + e.Step + ": " + e.Err.Error()
}

func (e *StepError) Unwrap() error { return e.Err }

func short(c string) string { return c[:min(7, len(c))] }

// BuildOptions configure one project build.
type BuildOptions struct {
	Name   string
	Source string // main or head
	Inputs []Input
	// Out is the snapshot directory to write (must not exist); it is
	// assembled beside it and renamed when complete.
	Out string
	// Work is the scratch directory archives extract into (removed by the
	// caller).
	Work       string
	PluginsDir string
	// PluginsDigest identifies the plugins (inputs digest).
	PluginsDigest string
	CacheDir      string
	// Self is the cartographer executable `extract scip` runs as.
	Self     string
	Mise     *Mise
	SchemaDB workspace.SchemaDB
	Digest   string
	// Version is the cartographer version (recorded with the tools).
	Version string
	Now     time.Time
	Log      io.Writer
}

// Build maps the inputs into a snapshot directory.
func Build(ctx context.Context, o BuildOptions) (*bundle.Manifest, error) {
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, format+"\n", a...)
		}
	}
	if o.Out == "" || o.Work == "" {
		return nil, errors.New("build: output and work directories required")
	}
	if _, err := os.Stat(o.Out); err == nil {
		return nil, fmt.Errorf("build: %s already exists", o.Out)
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Second)
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return nil, err
	}
	out, err := os.MkdirTemp(filepath.Dir(o.Out), "."+filepath.Base(o.Out)+".tmp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	if err := os.Chmod(out, 0o755); err != nil {
		return nil, err
	}
	for _, d := range []string{"facts", "docs", "annotations"} {
		if err := os.MkdirAll(filepath.Join(out, d), 0o755); err != nil {
			return nil, err
		}
	}
	m := &bundle.Manifest{
		SnapshotFormat: bundle.Format,
		ID:             bundle.SnapshotID(o.Name, now),
		CreatedAt:      now.Format(time.RFC3339),
		Workspace:      o.Name,
		Contexts:       bundle.ContextCfg{Depth: 2},
		Source:         o.Source,
		InputsDigest:   o.Digest,
		Tools:          map[string]string{},
	}
	inputs := append([]Input(nil), o.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name() < inputs[j].Name() })
	if len(o.Inputs) > 0 && o.Inputs[0].Settings.ContextsDepth > 0 {
		m.Contexts.Depth = o.Inputs[0].Settings.ContextsDepth // the anchor's
	}

	type tree struct {
		Input
		root  string
		files []string
	}
	var trees []tree
	var ignore []string
	for _, in := range inputs {
		root := filepath.Join(o.Work, "repos", in.Name())
		var files []string
		if in.Head {
			files, err = copyWorkingCopy(ctx, in.Path, root)
		} else {
			files, err = workspace.Archive(ctx, in.Path, in.Commit, root)
		}
		if err != nil {
			return nil, &StepError{in.Repo, in.Commit, "archive", err}
		}
		logf("%s @ %s: %d files", in.Name(), short(in.Commit), len(files))
		trees = append(trees, tree{in, root, files})
		ignore = append(ignore, root)
		m.Repos = append(m.Repos, bundle.Repo{Name: in.Name(), URL: in.URL(), Revision: in.Commit})
	}

	// toolEnv names the tools an extractor (or the schema step) runs with,
	// records them in the manifest and returns its environment.
	toolEnv := func(t tree, step string) ([]string, error) {
		tools, err := ExtractorTools(step, t.Pins)
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "tools", err}
		}
		if o.Mise == nil {
			return nil, &StepError{t.Repo, t.Commit, "tools", errors.New("no tool runner")}
		}
		env, err := o.Mise.Env(ctx, tools)
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "tools", err}
		}
		for _, tl := range tools {
			m.Tools[t.Name()+"/"+tl.Name] = tl.Version
		}
		return env, nil
	}
	// resolve makes argv[0] absolute through env's PATH (the tools'), not
	// the process PATH.
	resolve := func(env, argv []string) ([]string, error) {
		if filepath.IsAbs(argv[0]) {
			return argv, nil
		}
		p, err := lookIn(env, argv[0])
		if err != nil {
			return nil, err
		}
		return append([]string{p}, argv[1:]...), nil
	}

	schemaFacts := ""
	var schemaRepo *tree
	for i := range trees {
		if trees[i].Settings.Schema != nil {
			if schemaRepo != nil {
				return nil, &StepError{trees[i].Repo, trees[i].Commit, "settings", fmt.Errorf("[schema] is declared by %s too; one repo per project declares it", schemaRepo.Repo)}
			}
			schemaRepo = &trees[i]
		}
	}
	if t := schemaRepo; t != nil {
		sc := t.Settings.Schema
		env, err := toolEnv(*t, "schema")
		if err != nil {
			return nil, err
		}
		pd, err := workspace.SchemaPluginsDigest(sc.Capture, o.PluginsDir)
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "schema", err}
		}
		key, err := schemaKey(t.root, t.Commit, sc, pd, m.Tools, t.Name())
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "schema", err}
		}
		var wrapErr error
		cached, err := workspace.RunSchema(ctx, workspace.SchemaStep{
			Root:     t.root,
			Dir:      o.Work,
			Prepare:  sc.Prepare,
			Command:  sc.Capture,
			Vars:     map[string]string{"plugins": o.PluginsDir, "root": t.root, "repo": t.Name(), "revision": t.Commit, "workspace": o.Work},
			Env:      sc.Env,
			ExtraEnv: env,
			Key:      key,
			CacheDir: o.CacheDir,
			Wrap: func(argv []string) []string {
				a, err := resolve(env, argv)
				if err != nil {
					wrapErr = err
					return argv
				}
				return a
			},
			DB:  o.SchemaDB,
			Log: o.Log,
		})
		if err == nil {
			err = wrapErr
		}
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "schema", err}
		}
		if err := workspace.CopyFile(cached, filepath.Join(out, "facts", "schema.json")); err != nil {
			return nil, err
		}
		m.Facts = append(m.Facts, "facts/schema.json")
		schemaFacts = filepath.Join(out, "facts", "schema.json")
	}

	images := map[string]string{}
	for _, t := range trees {
		name := t.Name()
		for _, ex := range t.Settings.Extract {
			rel := fmt.Sprintf("facts/%s.%s.json", name, ex)
			step := "extract " + ex
			logf("%s: %s", name, step)
			var doc *facts.Doc
			switch ex {
			case "kubernetes":
				if doc, err = extract.Kubernetes(name, t.root, t.Commit); err != nil {
					return nil, &StepError{t.Repo, t.Commit, step, err}
				}
			default:
				env, err := toolEnv(t, ex)
				if err != nil {
					return nil, err
				}
				var argv []string
				roots := strings.Join(t.Settings.SourceRoots, ",")
				switch ex {
				case "go", "rust":
					argv = []string{o.Self, "extract", "scip", "--lang", ex, "--repo", name, "--root", t.root, "--revision", t.Commit}
					if ex == "rust" {
						argv = append(argv, fmt.Sprintf("--rust-build-scripts=%t", t.Settings.Rust.BuildScriptsOn()))
						if err := RustTargetAdd(ctx, env, o.Work); err != nil {
							return nil, &StepError{t.Repo, t.Commit, "tools", err}
						}
					}
				case "elixir":
					argv = []string{"elixir", filepath.Join(o.PluginsDir, "extract-elixir", "extract.exs"), "--repo", name, "--root", t.root, "--revision", t.Commit, "--source-roots", roots, "--schema-facts", schemaFacts}
				case "javascript":
					argv = []string{"node", filepath.Join(o.PluginsDir, "extract-javascript", "extract.mjs"), "--repo", name, "--root", t.root, "--revision", t.Commit, "--source-roots", roots}
				}
				argv = dropEmpty(argv)
				if argv, err = resolve(env, argv); err != nil {
					return nil, &StepError{t.Repo, t.Commit, "tools", err}
				}
				// Extractors run in the scratch directory, never inside an
				// archive, and with no repo or global mise config.
				stderr, err := workspace.RunPlugin(ctx, o.Work, env, argv, filepath.Join(out, rel), name)
				if s := strings.TrimSpace(stderr); s != "" {
					logf("%s: %s stderr:\n%s", name, step, workspace.Tail(s))
				}
				if err != nil {
					return nil, &StepError{t.Repo, t.Commit, step, err}
				}
				m.Facts = append(m.Facts, rel)
				continue
			}
			if err := writeDoc(filepath.Join(out, rel), doc); err != nil {
				return nil, err
			}
			m.Facts = append(m.Facts, rel)
		}
		// URLs in configuration, for every repo.
		doc, err := extract.ConfigURLs(name, t.root, t.Commit)
		if err != nil {
			return nil, &StepError{t.Repo, t.Commit, "extract urls", err}
		}
		if len(doc.ConfigURLs) > 0 {
			rel := fmt.Sprintf("facts/%s.urls.json", name)
			if err := writeDoc(filepath.Join(out, rel), doc); err != nil {
				return nil, err
			}
			m.Facts = append(m.Facts, rel)
		}

		exts := map[string]int{}
		for _, f := range t.files {
			exts[path.Ext(f)]++
			if !t.Settings.Docs.None && workspace.IsDoc(f, t.Settings.Docs.Patterns) {
				if err := workspace.CopyFile(filepath.Join(t.root, filepath.FromSlash(f)), filepath.Join(out, "docs", name, filepath.FromSlash(f))); err != nil {
					return nil, err
				}
			}
			if dir, file := path.Split(f); dir == AnnotationsDir+"/" && strings.HasSuffix(file, ".json") {
				if err := workspace.CopyFile(filepath.Join(t.root, filepath.FromSlash(f)), filepath.Join(out, "annotations", name, file)); err != nil {
					return nil, err
				}
			}
		}
		keys := make([]string, 0, len(exts))
		for k := range exts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			m.Inventory = append(m.Inventory, bundle.Inventory{Repo: name, Ext: k, Files: exts[k]})
		}
		for _, img := range t.Settings.Images {
			if prev, ok := images[img]; ok && prev != name {
				return nil, &StepError{t.Repo, t.Commit, "settings", fmt.Errorf("image %q is claimed by %s too", img, prev)}
			}
			images[img] = name
		}
	}
	if len(images) > 0 {
		m.Images = images
	}
	if o.Version != "" {
		m.Tools["cartographer"] = o.Version
	}
	if o.PluginsDigest != "" {
		m.Tools["plugins"] = o.PluginsDigest
	}
	if len(m.Tools) == 0 {
		m.Tools = nil
	}

	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	if _, err := bundle.Read(out); err != nil {
		return nil, &StepError{Step: "assemble", Err: err}
	}
	if err := os.Rename(out, o.Out); err != nil {
		return nil, err
	}
	logf("snapshot %s: %d facts documents", m.ID, len(m.Facts))
	return m, nil
}

// dropEmpty removes a --flag whose value is "" (an optional input).
func dropEmpty(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "--") && i+1 < len(argv) && argv[i+1] == "" && !strings.Contains(argv[i], "=") {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func writeDoc(p string, d *facts.Doc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return workspace.WriteAtomic(p, append(b, '\n'))
}

// schemaKey is the schema cache key: the step's unexpanded commands and
// env, the plugin code, the tools, and the files under cache_key in the
// archive (each must match something), or the commit when there is none.
func schemaKey(root, commit string, sc *SchemaSettings, pluginsDigest string, tools map[string]string, repo string) (string, error) {
	h := sha256.New()
	env, _ := json.Marshal(sc.Env) // map keys sort
	fmt.Fprintf(h, "project-schema v1\nprepare=%q\ncapture=%q\nenv=%s\nplugins=%s\n", sc.Prepare, sc.Capture, env, pluginsDigest)
	var keys []string
	for k := range tools {
		if strings.HasPrefix(k, repo+"/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "tool %s=%s\n", strings.TrimPrefix(k, repo+"/"), tools[k])
	}
	if len(sc.CacheKey) == 0 {
		fmt.Fprintf(h, "revision=%s\n", commit)
	}
	for _, p := range sc.CacheKey {
		base := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		n := 0
		err := filepath.WalkDir(base, func(f string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, f)
			fmt.Fprintf(h, "file %s %d\n", filepath.ToSlash(rel), len(b))
			h.Write(b)
			n++
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if n == 0 {
			// A mistyped path would freeze the cache forever.
			return "", fmt.Errorf("schema cache_key %q matches nothing", p)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyWorkingCopy copies a checkout's tracked and untracked (not ignored)
// regular files into dst, as they are on disk.
func copyWorkingCopy(ctx context.Context, src, dst string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", src, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range strings.Split(out.String(), "\x00") {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		p := filepath.Join(src, filepath.FromSlash(f))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue // deleted, a symlink or a submodule
		}
		if err := workspace.CopyFile(p, filepath.Join(dst, filepath.FromSlash(f))); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}
