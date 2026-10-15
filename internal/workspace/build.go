package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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
	// PluginsDigest identifies the plugin code for the schema cache key;
	// empty means a digest of PluginsDir's files.
	PluginsDigest string
	// CacheDir holds cached schema facts (default user cache dir).
	CacheDir string
	// Refs overrides repo refs: name -> revision (e.g. a PR head).
	Refs map[string]string
	// Now is the build time (tests); zero means time.Now.
	Now time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// Fetch clones missing checkouts from their URL and fetches existing
	// ones (and any Refs revisions) before building. A GH_TOKEN/GITHUB_TOKEN
	// in the environment authenticates https://github.com URLs; it is passed
	// to git through its environment only (never argv), and is removed from
	// the environment of plugins and the schema prepare command.
	Fetch bool
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
		if o.Fetch {
			if err := fetchRepo(ctx, r, src, o.Refs[r.Name]); err != nil {
				return nil, fmt.Errorf("build: %s: fetch: %w", r.Name, err)
			}
		}
		rev, err := git(ctx, src, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
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
		pdigest := o.PluginsDigest
		if pdigest == "" && o.PluginsDir != "" {
			if pdigest, err = dirDigest(o.PluginsDir); err != nil {
				return nil, fmt.Errorf("build: plugins digest: %w", err)
			}
		}
		key, err := schemaKey(ctx, c, prep.revision, prep.repo, pdigest)
		if err != nil {
			return nil, err
		}
		cached := filepath.Join(o.CacheDir, "schema", key+".json")
		if err := validFacts(cached); err == nil {
			logf("schema: cached %s", key[:12])
		} else {
			if !errors.Is(err, os.ErrNotExist) {
				logf("schema: cached %s is unusable (%v); rebuilding", key[:12], err)
			}
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

	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
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

// git runs git in dir. Credentials reach git only through its environment
// (gitEnv), and error text carries the arguments and stderr, redacted.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", errors.New(redact(fmt.Sprintf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))))
	}
	return out.String(), nil
}

// archive extracts the tracked files of rev into dst and returns their paths.
func archive(ctx context.Context, src, rev, dst string) ([]string, error) {
	// A blobless clone fetches blobs lazily here, so this needs auth too.
	cmd := exec.CommandContext(ctx, "git", "-C", src, "archive", "--format=tar", rev)
	cmd.Env = gitEnv()
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
			if err := os.MkdirAll(target, 0o755); err != nil {
				cmd.Wait()
				return nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				cmd.Wait()
				return nil, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				cmd.Wait()
				return nil, err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				cmd.Wait()
				return nil, err
			}
			files = append(files, name)
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, errors.New(redact(fmt.Sprintf("%v: %s", err, strings.TrimSpace(errb.String()))))
	}
	sort.Strings(files)
	return files, nil
}

func schemaKey(ctx context.Context, c *Config, rev string, r Repo, pluginsDigest string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "schema-version=%s\ncommand=%q\nprepare=%q\nprepare-repo=%q\nenv=%q\nplugins=%s\n",
		c.Schema.Version, c.Schema.Command, c.Schema.Prepare, c.Schema.PrepareRepo, mapEnv(c.Schema.Env), pluginsDigest)
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
	cmd.Env = append(childEnv(os.Environ()), env...)
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
	return writeAtomic(out, buf.Bytes())
}

// writeAtomic writes b to a temporary file beside path and renames it into
// place, so readers (and a later cache lookup) never see a partial file.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// validFacts reports whether path holds a valid facts document.
func validFacts(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d, err := facts.Decode(f)
	if err != nil {
		return err
	}
	return d.Validate()
}

// dirDigest hashes the relative paths and contents of every file under dir.
func dirDigest(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
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

// fetchRepo clones r into dir when missing, otherwise fetches origin; an
// extra revision (e.g. a pull request head) is fetched explicitly.
func fetchRepo(ctx context.Context, r Repo, dir, extra string) error {
	if strings.HasPrefix(extra, "-") {
		return fmt.Errorf("invalid revision %q", extra)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if r.URL == "" {
			return fmt.Errorf("%s is missing and the repo has no url", dir)
		}
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, parent, "clone", "--quiet", "--filter=blob:none", "--no-checkout", "--", r.URL, dir); err != nil {
			return err
		}
	} else if _, err := git(ctx, dir, "fetch", "--quiet", "--", "origin"); err != nil {
		return err
	}
	if extra != "" {
		if _, err := git(ctx, dir, "fetch", "--quiet", "--", "origin", extra); err != nil {
			return err
		}
	}
	return nil
}

// tokenEnv are the credential variables that only git subprocesses receive.
var tokenEnv = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}

func githubToken() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

const extraHeaderKey = "http.https://github.com/.extraheader"

// gitEnv is the environment for git subprocesses: the process environment
// plus, when a GitHub token is set, an extraheader config entry passed as
// GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n so it never appears on argv. Every git
// process (including lazy blob fetches of a partial clone) inherits it.
func gitEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	tok := githubToken()
	if tok == "" {
		return env
	}
	n := 0
	if v, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && v > 0 {
		n = v
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return append(env,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, extraHeaderKey),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=AUTHORIZATION: basic %s", n, basic),
	)
}

// childEnv drops credentials and git config injection from env, for plugins
// and the schema prepare command (which may run a pull request's code).
func childEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := slices.Contains(tokenEnv, k) || k == "GIT_CONFIG_COUNT" || k == "GIT_CONFIG_PARAMETERS" ||
			strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_")
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// redact removes credentials from text destined for logs and errors: the
// token itself, its base64 form, and anything that looks like an
// extraheader or Authorization value.
func redact(s string) string {
	if tok := githubToken(); tok != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
		s = strings.ReplaceAll(s, basic, "[redacted]")
		s = strings.ReplaceAll(s, tok, "[redacted]")
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), "extraheader") || strings.Contains(strings.ToLower(l), "authorization:") {
			lines[i] = redactLine(l)
		}
	}
	return strings.Join(lines, "\n")
}

func redactLine(l string) string {
	low := strings.ToLower(l)
	for _, kw := range []string{"extraheader", "authorization:"} {
		if i := strings.Index(low, kw); i >= 0 {
			return l[:i+len(kw)] + " [redacted]"
		}
	}
	return l
}
