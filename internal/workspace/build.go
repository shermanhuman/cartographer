package workspace

import (
	"archive/tar"
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
	"github.com/shermanhuman/cartographer/internal/facts"
)

type BuildOptions struct {
	// Out is the snapshot directory to write (created; must not exist).
	Out string
	// PluginsDir replaces {plugins} in plugin commands.
	PluginsDir string
	// CacheDir holds cached schema facts (default user cache dir).
	CacheDir string
	// Refs overrides repo refs: name -> revision (e.g. a PR head).
	Refs map[string]string
	// Now is the build time (tests); zero means time.Now.
	Now time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// Build snapshots every repo with git archive, runs the plugins and writes a
// snapshot directory that bundle.Read accepts.
func Build(ctx context.Context, c *Config, o BuildOptions) (*bundle.Manifest, error) {
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, format+"\n", a...)
		}
	}
	if o.Out == "" {
		return nil, errors.New("build: output directory required")
	}
	if _, err := os.Stat(o.Out); err == nil {
		return nil, fmt.Errorf("build: %s already exists", o.Out)
	}
	if o.CacheDir == "" {
		o.CacheDir = DefaultCacheDir()
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Second)

	work, err := os.MkdirTemp("", "cartographer-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	for _, d := range []string{"facts", "docs", "annotations"} {
		if err := os.MkdirAll(filepath.Join(o.Out, d), 0o755); err != nil {
			return nil, err
		}
	}

	m := &bundle.Manifest{
		SnapshotFormat: bundle.Format,
		ID:             "snapshot-" + now.Format("20060102T150405Z"),
		CreatedAt:      now.Format(time.RFC3339),
		Workspace:      c.Name,
		Contexts:       bundle.ContextCfg{Depth: c.Contexts.Depth},
	}

	type checkout struct {
		repo     Repo
		root     string
		revision string
		files    []string
	}
	var cos []checkout
	for _, r := range c.Repos {
		ref := r.Ref
		if v, ok := o.Refs[r.Name]; ok {
			ref = v
		}
		src := c.RepoPath(r)
		rev, err := git(ctx, src, "rev-parse", "--verify", ref+"^{commit}")
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", r.Name, err)
		}
		rev = strings.TrimSpace(rev)
		root := filepath.Join(work, "repos", r.Name)
		files, err := archive(ctx, src, rev, root)
		if err != nil {
			return nil, fmt.Errorf("build: %s: archive: %w", r.Name, err)
		}
		logf("%s @ %s: %d files", r.Name, rev[:min(12, len(rev))], len(files))
		cos = append(cos, checkout{r, root, rev, files})
		m.Repos = append(m.Repos, bundle.Repo{Name: r.Name, URL: r.URL, Revision: rev})
	}

	vars := map[string]string{"plugins": o.PluginsDir, "workspace": c.dir, "schema_facts": ""}
	if c.Schema != nil {
		var prep checkout
		for _, co := range cos {
			if co.repo.Name == c.Schema.PrepareRepo {
				prep = co
			}
		}
		key, err := schemaKey(ctx, c, prep.revision, prep.repo)
		if err != nil {
			return nil, err
		}
		cached := filepath.Join(o.CacheDir, "schema", key+".json")
		if _, err := os.Stat(cached); err == nil {
			logf("schema: cached %s", key[:12])
		} else {
			logf("schema: building (cache key %s)", key[:12])
			env := mapEnv(c.Schema.Env)
			if c.Schema.Prepare != "" {
				if _, err := runCmd(ctx, prep.root, env, []string{"sh", "-c", c.Schema.Prepare}, nil); err != nil {
					return nil, fmt.Errorf("build: schema prepare: %w", err)
				}
			}
			v := map[string]string{"plugins": o.PluginsDir, "workspace": c.dir, "root": prep.root, "repo": prep.repo.Name, "revision": prep.revision}
			if err := runPlugin(ctx, c.dir, env, expand(c.Schema.Command, v), cached, ""); err != nil {
				return nil, fmt.Errorf("build: schema: %w", err)
			}
		}
		if err := copyFile(cached, filepath.Join(o.Out, "facts", "schema.json")); err != nil {
			return nil, err
		}
		m.Facts = append(m.Facts, "facts/schema.json")
		vars["schema_facts"] = filepath.Join(o.Out, "facts", "schema.json")
	}

	for _, co := range cos {
		for _, pname := range co.repo.Plugins {
			p := c.Plugins[pname]
			v := map[string]string{"repo": co.repo.Name, "root": co.root, "revision": co.revision}
			for k, val := range vars {
				v[k] = val
			}
			rel := fmt.Sprintf("facts/%s.%s.json", co.repo.Name, pname)
			logf("%s: plugin %s", co.repo.Name, pname)
			// Plugins run in the workspace directory, never inside the archived
			// repository: its tool config (e.g. mise.toml) is not trusted here.
			if err := runPlugin(ctx, c.dir, mapEnv(p.Env), expand(p.Command, v), filepath.Join(o.Out, rel), co.repo.Name); err != nil {
				return nil, fmt.Errorf("build: %s: plugin %s: %w", co.repo.Name, pname, err)
			}
			m.Facts = append(m.Facts, rel)
		}
		for _, f := range co.files {
			if !isDoc(f, co.repo.Docs) {
				continue
			}
			if err := copyFile(filepath.Join(co.root, filepath.FromSlash(f)), filepath.Join(o.Out, "docs", co.repo.Name, filepath.FromSlash(f))); err != nil {
				return nil, err
			}
		}
	}

	anns, _ := filepath.Glob(filepath.Join(c.dir, c.Annotations, "*.json"))
	sort.Strings(anns)
	for _, a := range anns {
		if err := copyFile(a, filepath.Join(o.Out, "annotations", filepath.Base(a))); err != nil {
			return nil, err
		}
	}

	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(o.Out, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	if _, err := bundle.Read(o.Out); err != nil {
		return nil, fmt.Errorf("build: produced an invalid snapshot: %w", err)
	}
	logf("snapshot %s: %d facts documents", m.ID, len(m.Facts))
	return m, nil
}

// DefaultCacheDir is $XDG_CACHE_HOME/cartographer or ~/.cache/cartographer.
func DefaultCacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "cartographer")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "cartographer")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// archive extracts the tracked files of rev into dst and returns their paths.
func archive(ctx context.Context, src, rev, dst string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", src, "archive", "--format=tar", rev)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var files []string
	tr := tar.NewReader(stdout)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cmd.Wait()
			return nil, err
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "../") || path.IsAbs(name) {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return nil, err
			}
			files = append(files, name)
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, errb.String())
	}
	sort.Strings(files)
	return files, nil
}

func schemaKey(ctx context.Context, c *Config, rev string, r Repo) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "schema-version=%s\ncommand=%q\nprepare=%q\n", c.Schema.Version, c.Schema.Command, c.Schema.Prepare)
	if r.Name != "" && len(c.Schema.CacheKey) > 0 {
		out, err := git(ctx, c.RepoPath(r), append([]string{"ls-tree", "-r", rev, "--"}, c.Schema.CacheKey...)...)
		if err != nil {
			return "", err
		}
		h.Write([]byte(out))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// expand substitutes {placeholders}. An argument that expands to "" is
// dropped together with a preceding --flag, so optional inputs disappear.
func expand(cmd []string, v map[string]string) []string {
	var out []string
	for _, a := range cmd {
		empty := false
		for k, val := range v {
			if strings.Contains(a, "{"+k+"}") {
				a = strings.ReplaceAll(a, "{"+k+"}", val)
				if val == "" && a == "" {
					empty = true
				}
			}
		}
		if empty {
			if n := len(out); n > 0 && strings.HasPrefix(out[n-1], "--") {
				out = out[:n-1]
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func mapEnv(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var env []string
	for _, k := range keys {
		env = append(env, k+"="+m[k])
	}
	return env
}

func runCmd(ctx context.Context, dir string, env, argv []string, stdout io.Writer) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if err := cmd.Run(); err != nil {
		tail := errb.String()
		if len(tail) > 2000 {
			tail = "…" + tail[len(tail)-2000:]
		}
		return "", fmt.Errorf("%s: %v\n%s", argv[0], err, tail)
	}
	return errb.String(), nil
}

// runPlugin runs an extractor, validates its facts output and writes it.
func runPlugin(ctx context.Context, dir string, env, argv []string, out, repo string) error {
	var buf bytes.Buffer
	if _, err := runCmd(ctx, dir, env, argv, &buf); err != nil {
		return err
	}
	d, err := facts.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if repo != "" && d.Repo != repo {
		return fmt.Errorf("facts repo is %q, want %q", d.Repo, repo)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

func isDoc(file string, patterns []string) bool {
	if !strings.HasSuffix(file, ".md") {
		return false
	}
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, file); ok || strings.HasPrefix(file, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
