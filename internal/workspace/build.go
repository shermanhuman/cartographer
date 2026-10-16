package workspace

import (
	"archive/tar"
	"bytes"
	"cmp"
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
	"github.com/shermanhuman/cartographer/internal/fresh"
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
	// Paths overrides repo checkouts: name -> directory (see Local).
	Paths map[string]string
	// Now is the build time (tests); zero means time.Now.
	Now time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// Fetch clones missing checkouts from their URL and fetches existing
	// ones (and any Refs revisions) before building; a configured ref then
	// resolves against origin (origin/HEAD, origin/<branch>), since a fetch
	// does not move a checkout's local branches. A GH_TOKEN/GITHUB_TOKEN in
	// the environment authenticates https://github.com URLs; it is passed to
	// git through its environment only (never argv), and is removed from the
	// environment of plugins and the schema prepare command. Those run as
	// the same user, though, and can read the parent's environment: when
	// they may run untrusted code (a pull request), use Fetch in a separate
	// process that holds the token, then Build without it.
	Fetch bool
}

func logger(w io.Writer) func(string, ...any) {
	return func(format string, a ...any) {
		if w != nil {
			fmt.Fprintf(w, format+"\n", a...)
		}
	}
}

// Fetch clones or fetches every repo (and any refs revisions) and downloads
// the files of the revision a later Build without Fetch will snapshot, so
// that build needs neither the network nor credentials (a blobless clone
// otherwise fetches file contents lazily). It runs no plugin and no prepare
// command: CI gives the token to this step only (build --fetch-only).
func Fetch(ctx context.Context, c *Config, refs map[string]string, log io.Writer) error {
	logf := logger(log)
	for _, r := range c.Repos {
		src := c.RepoPath(r)
		if err := fetchRepo(ctx, r, src, refs[r.Name]); err != nil {
			return fmt.Errorf("fetch: %s: %w", r.Name, err)
		}
		rev, err := revision(ctx, r, src, refs, false)
		if err != nil {
			return fmt.Errorf("fetch: %s: %w", r.Name, err)
		}
		// fetchRepo moves the local branches of a managed clone only; a
		// developer's own checkout keeps its branch, which a later Build
		// without Fetch would snapshot.
		if upstream, err := revision(ctx, r, src, refs, true); err == nil && upstream != rev {
			return fmt.Errorf("fetch: %s: %s differs from origin (local %.12s, origin %.12s), and --fetch-only leaves your own checkout alone: bring it in line (pull or push), or use build --fetch to build origin's revision", r.Name, r.Ref, rev, upstream)
		}
		if err := primeBlobs(ctx, src, rev); err != nil {
			return fmt.Errorf("fetch: %s: %w", r.Name, err)
		}
		logf("%s @ %s: fetched", r.Name, rev[:min(12, len(rev))])
	}
	return nil
}

// Local resolves the checkouts a local build (build --local) snapshots at
// their HEAD: a [checkouts] entry from the user config (explicit), else the
// configured path when it is a git checkout, else a sibling of the workspace
// directory matched by origin URL or name, as freshness locates them. No
// fetch and no credentials: the map covers what the developer has. Every
// repo must be found.
func Local(ctx context.Context, c *Config, explicit map[string]string) (paths, refs map[string]string, err error) {
	paths, refs = map[string]string{}, map[string]string{}
	var rest []fresh.Repo
	for _, r := range c.Repos {
		switch p, ok := explicit[r.Name]; {
		case ok:
			paths[r.Name] = fresh.ExpandHome(p)
		case isCheckout(c.RepoPath(r)):
			paths[r.Name] = c.RepoPath(r)
		default:
			rest = append(rest, fresh.Repo{Name: r.Name, URL: r.URL})
		}
	}
	if len(rest) > 0 {
		for name, p := range fresh.LocateFrom(ctx, rest, fresh.Find(ctx, c.dir), nil) {
			paths[name] = p
		}
	}
	var missing []string
	for _, r := range c.Repos {
		if p := paths[r.Name]; p == "" || !isCheckout(p) {
			missing = append(missing, fmt.Sprintf("%s (looked at %s)", r.Name, cmp.Or(p, c.RepoPath(r))))
			continue
		}
		refs[r.Name] = "HEAD"
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("build --local: no checkout for %s; clone it beside the workspace or list it under [checkouts] in the user config", strings.Join(missing, ", "))
	}
	return paths, refs, nil
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Build snapshots every repo with git archive, runs the plugins and writes a
// snapshot directory that bundle.Read accepts. The snapshot is assembled in
// a temporary directory beside Out and renamed to Out only when complete, so
// a failed or cancelled build leaves nothing behind.
func Build(ctx context.Context, c *Config, o BuildOptions) (*bundle.Manifest, error) {
	logf := logger(o.Log)
	logStderr := func(what, stderr string) {
		if stderr = strings.TrimSpace(stderr); stderr != "" {
			logf("%s stderr:\n%s", what, tail(stderr))
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
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return nil, err
	}
	out, err := os.MkdirTemp(filepath.Dir(o.Out), "."+filepath.Base(o.Out)+".tmp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out) // a no-op once renamed to o.Out
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
		ID:             bundle.SnapshotID(c.Name, now),
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
		src := c.RepoPath(r)
		if p, ok := o.Paths[r.Name]; ok {
			src = p
		}
		if o.Fetch {
			if err := fetchRepo(ctx, r, src, o.Refs[r.Name]); err != nil {
				return nil, fmt.Errorf("build: %s: fetch: %w", r.Name, err)
			}
		}
		rev, err := revision(ctx, r, src, o.Refs, o.Fetch)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", r.Name, err)
		}
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
			if pdigest, err = DirDigest(o.PluginsDir); err != nil {
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
				stderr, err := runCmd(ctx, prep.root, env, []string{"sh", "-c", c.Schema.Prepare}, nil)
				if err != nil {
					return nil, fmt.Errorf("build: schema prepare: %w", err)
				}
				logStderr("schema prepare", stderr)
			}
			v := map[string]string{"plugins": o.PluginsDir, "workspace": c.dir, "root": prep.root, "repo": prep.repo.Name, "revision": prep.revision}
			stderr, err := runPlugin(ctx, c.dir, env, expand(c.Schema.Command, v), cached, "")
			if err != nil {
				return nil, fmt.Errorf("build: schema: %w", err)
			}
			logStderr("schema", stderr)
		}
		if err := copyFile(cached, filepath.Join(out, "facts", "schema.json")); err != nil {
			return nil, err
		}
		m.Facts = append(m.Facts, "facts/schema.json")
		vars["schema_facts"] = filepath.Join(out, "facts", "schema.json")
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
			stderr, err := runPlugin(ctx, c.dir, mapEnv(p.Env), expand(p.Command, v), filepath.Join(out, rel), co.repo.Name)
			if err != nil {
				return nil, fmt.Errorf("build: %s: plugin %s: %w", co.repo.Name, pname, err)
			}
			logStderr(fmt.Sprintf("%s: plugin %s", co.repo.Name, pname), stderr)
			m.Facts = append(m.Facts, rel)
		}
		for _, f := range co.files {
			if !isDoc(f, co.repo.Docs) {
				continue
			}
			if err := copyFile(filepath.Join(co.root, filepath.FromSlash(f)), filepath.Join(out, "docs", co.repo.Name, filepath.FromSlash(f))); err != nil {
				return nil, err
			}
		}
	}

	anns, _ := filepath.Glob(filepath.Join(c.dir, c.Annotations, "*.json"))
	sort.Strings(anns)
	for _, a := range anns {
		if err := copyFile(a, filepath.Join(out, "annotations", filepath.Base(a))); err != nil {
			return nil, err
		}
	}

	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	if _, err := bundle.Read(out); err != nil {
		return nil, fmt.Errorf("build: produced an invalid snapshot: %w", err)
	}
	if err := os.Rename(out, o.Out); err != nil {
		return nil, fmt.Errorf("build: %w", err)
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

// revision resolves the commit to snapshot: an override from refs as given,
// else the configured ref. After a fetch the configured ref resolves against
// origin when origin has it (HEAD -> origin/HEAD, main -> origin/main),
// because fetching moves origin/*, not a checkout's local branches.
func revision(ctx context.Context, r Repo, src string, refs map[string]string, fetched bool) (string, error) {
	ref, override := refs[r.Name]
	if !override {
		ref = r.Ref
		if fetched {
			remote := "refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")
			if out, err := git(ctx, src, "rev-parse", "--verify", "--quiet", "--end-of-options", remote+"^{commit}"); err == nil {
				return strings.TrimSpace(out), nil
			}
		}
	}
	out, err := git(ctx, src, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// primeBlobs reads every file of rev once, which makes a blobless clone
// download them now (with credentials) rather than during a later archive.
func primeBlobs(ctx context.Context, src, rev string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", src, "archive", "--format=tar", rev)
	cmd.Env = gitEnv()
	cmd.Stdout = io.Discard
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return errors.New(redact(fmt.Sprintf("git archive %s: %v: %s", rev, err, strings.TrimSpace(errb.String()))))
	}
	return nil
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
	// On an error, stop git before waiting: an undrained pipe would block it
	// (and Wait) forever.
	abort := func(err error) error {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	var files []string
	tr := tar.NewReader(stdout)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, abort(err)
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "../") || path.IsAbs(name) {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, abort(err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, abort(err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return nil, abort(err)
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, abort(err)
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
	if r.Name != "" && len(c.Schema.CacheKey) == 0 {
		// Nothing narrower configured: any change to the repo misses.
		fmt.Fprintf(h, "revision=%s\n", rev)
	}
	if r.Name != "" {
		for _, p := range c.Schema.CacheKey {
			out, err := git(ctx, c.RepoPath(r), "ls-tree", "-r", rev, "--", p)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(out) == "" {
				// A mistyped path would freeze the cache forever.
				return "", fmt.Errorf("build: schema cache_key %q matches nothing in %s at %s", p, r.Name, rev[:min(12, len(rev))])
			}
			fmt.Fprintf(h, "cache_key=%q\n%s", p, out)
		}
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
	// Once cancelled, don't wait long for grandchildren (sh -c) that keep
	// the output pipes open.
	cmd.WaitDelay = 5 * time.Second
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w\n%s", argv[0], err, tail(errb.String()))
	}
	return errb.String(), nil
}

// tail keeps the last 2000 bytes of command output for logs and errors.
func tail(s string) string {
	if len(s) > 2000 {
		return "…" + s[len(s)-2000:]
	}
	return s
}

// runPlugin runs an extractor, validates its facts output and writes it. It
// returns the extractor's stderr (warnings) for the caller to log.
func runPlugin(ctx context.Context, dir string, env, argv []string, out, repo string) (string, error) {
	var buf bytes.Buffer
	stderr, err := runCmd(ctx, dir, env, argv, &buf)
	if err != nil {
		return "", err
	}
	d, err := facts.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return stderr, err
	}
	if err := d.Validate(); err != nil {
		return stderr, err
	}
	if repo != "" && d.Repo != repo {
		return stderr, fmt.Errorf("facts repo is %q, want %q", d.Repo, repo)
	}
	return stderr, writeAtomic(out, buf.Bytes())
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

// DirDigest hashes the relative paths and contents of every file under dir
// (16 hex digits), e.g. for BuildOptions.PluginsDigest.
func DirDigest(dir string) (string, error) {
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

// managedKey marks a clone fetchRepo created, whose local branches it may move.
const managedKey = "cartographer.managed"

// fetchRepo clones r into dir when missing, otherwise fetches origin; an
// extra revision (e.g. a pull request head) is fetched explicitly. The clone
// stores no credential: auth reaches git only through gitEnv.
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
		if _, err := git(ctx, dir, "config", managedKey, "true"); err != nil {
			return err
		}
	} else if out, _ := git(ctx, dir, "config", "--bool", managedKey); strings.TrimSpace(out) == "true" {
		// A clone cartographer made (no work tree to protect): move its local
		// branches too, so a later build without fetch sees upstream.
		if _, err := git(ctx, dir, "fetch", "--quiet", "--update-head-ok", "--", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
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
