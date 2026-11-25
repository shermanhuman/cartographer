package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Helpers shared with the project builder (package project), which maps
// registered projects from fetched commits: the same archive, plugin,
// schema and assembly steps as Build.

// Archive extracts the tracked files of rev in the git checkout src into
// dst and returns their slash-separated paths, sorted.
func Archive(ctx context.Context, src, rev, dst string) ([]string, error) {
	return archive(ctx, src, rev, dst)
}

// RunPlugin runs an extractor in dir (env added to the process
// environment, credentials removed), validates the facts document it
// prints (its repo must be repo when repo is set) and writes it to out. It
// returns the extractor's stderr for the caller to log.
func RunPlugin(ctx context.Context, dir string, env, argv []string, out, repo string) (string, error) {
	return runPlugin(ctx, dir, env, argv, out, repo)
}

// RunCmd runs argv in dir in its own process group (cancelling ends the
// group) and returns its stderr; stdout goes to stdout when set.
func RunCmd(ctx context.Context, dir string, env, argv []string, stdout io.Writer) (string, error) {
	return runCmd(ctx, dir, env, argv, stdout)
}

// RunGroup runs a prepared command in its own process group, as RunCmd
// does, for callers that set up its environment and output themselves.
func RunGroup(cmd *exec.Cmd) error {
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	g := ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	g.started()
	err := cmd.Wait()
	g.done()
	return err
}

// Expand substitutes {placeholders} in a command (see Plugin).
func Expand(cmd []string, v map[string]string) []string { return expand(cmd, v) }

// MapEnv renders env as sorted KEY=VALUE pairs.
func MapEnv(m map[string]string) []string { return mapEnv(m) }

// IsDoc reports whether a tracked file is Markdown the docs patterns include
// (no patterns: every *.md).
func IsDoc(file string, patterns []string) bool { return isDoc(file, patterns) }

// CopyFile copies src to dst, creating dst's directory.
func CopyFile(src, dst string) error { return copyFile(src, dst) }

// WriteAtomic writes b to path through a temporary file and a rename.
func WriteAtomic(path string, b []byte) error { return writeAtomic(path, b) }

// Tail keeps the last 2000 bytes of command output.
func Tail(s string) string { return tail(s) }

// ChildEnv drops credentials and git config injection from env.
func ChildEnv(env []string) []string { return childEnv(env) }

// SchemaDB provides a database for one uncached schema run (e.g. a
// throwaway PostgreSQL cluster). Start returns placeholder values ({pg_port})
// expanded in the step's Env and Command, and stop, which must remove what
// Start made, also after a failure or cancel.
type SchemaDB interface {
	Start(ctx context.Context, scratch string) (vars map[string]string, stop func(), err error)
}

// KeyedSchemaDB is a SchemaDB whose identity (e.g. its server version)
// belongs in the schema cache key.
type KeyedSchemaDB interface {
	SchemaDB
	Key() string
}

// TrackGroup registers a long-running process group that the caller
// started (and stops) itself, such as a database server, so KillGroups ends
// it too; untrack forgets it once it has exited.
func TrackGroup(pgid int) (untrack func()) { return trackGroup(pgid) }

// SchemaStep is one schema extraction: Prepare (a shell command) runs in
// Root, then Command runs in Dir and prints the schema facts document, which
// is cached under CacheDir/schema/<Key>.json. Both are skipped when that file
// is already valid, so the key must cover every input (templates
// unexpanded).
type SchemaStep struct {
	Root     string
	Dir      string
	Prepare  string
	Command  []string          // placeholders expand with Vars
	Vars     map[string]string // {plugins} {root} {repo} {revision} …
	Env      map[string]string // values expand with the SchemaDB's vars
	ExtraEnv []string          // added as is (e.g. the tool runner's)
	Key      string
	CacheDir string
	// Wrap wraps each command before it runs (e.g. `mise exec <tools> --`);
	// nil runs it as is.
	Wrap func(argv []string) []string
	// DB, when set, is started for an uncached run; nil uses whatever
	// database Env names (the behaviour of cartographer 0.1).
	DB SchemaDB
	// Scratch is where DB's directory is made (default: the system temp
	// directory); it is removed after the run.
	Scratch string
	Log     io.Writer
}

// RunSchema runs the step unless its cached facts are valid, and returns
// the cached facts file.
func RunSchema(ctx context.Context, st SchemaStep) (string, error) {
	logf := logger(st.Log)
	cached := filepath.Join(st.CacheDir, "schema", st.Key+".json")
	if err := validFacts(cached); err == nil {
		logf("schema: cached %s", st.Key[:min(12, len(st.Key))])
		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		logf("schema: cached %s is unusable (%v); rebuilding", st.Key[:min(12, len(st.Key))], err)
	}
	logf("schema: building (cache key %s)", st.Key[:min(12, len(st.Key))])
	vars := map[string]string{}
	for k, v := range st.Vars {
		vars[k] = v
	}
	dbVars := map[string]string{}
	if st.DB != nil {
		scratch, err := os.MkdirTemp(st.Scratch, "cartographer-schema-db-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(scratch)
		got, stop, err := st.DB.Start(ctx, scratch)
		if err != nil {
			return "", fmt.Errorf("build: schema database: %w", err)
		}
		defer stop()
		for k, v := range got {
			vars[k], dbVars[k] = v, v
		}
	}
	// Only the database's placeholders expand in Env (0.1 expanded none).
	envMap := map[string]string{}
	for k, v := range st.Env {
		for vk, vv := range dbVars {
			v = strings.ReplaceAll(v, "{"+vk+"}", vv)
		}
		envMap[k] = v
	}
	// The process environment, then ExtraEnv (the tools'), then the repo's
	// own [schema] env, so the repo's values win over the caller's (a
	// TEST_DB_PORT or MIX_ENV in an MCP or shell environment). A step with
	// its own database sees none of libpq's PG* variables from the caller.
	env := append(childEnv(os.Environ()), st.ExtraEnv...)
	if st.DB != nil {
		env = WithoutPG(env)
	}
	env = append(env, mapEnv(envMap)...)
	if st.Scratch != "" {
		// Last: prepare's and capture's temp files go in the build's scratch.
		env = append(env, "TMPDIR="+st.Scratch)
	}
	wrap := st.Wrap
	if wrap == nil {
		wrap = func(a []string) []string { return a }
	}
	if st.Prepare != "" {
		stderr, err := runCmdIn(ctx, st.Root, env, wrap([]string{"sh", "-c", st.Prepare}), nil)
		if err != nil {
			return "", fmt.Errorf("build: schema prepare: %w", err)
		}
		if stderr = strings.TrimSpace(stderr); stderr != "" {
			logf("schema prepare stderr:\n%s", tail(stderr))
		}
	}
	stderr, err := runPluginIn(ctx, st.Dir, env, wrap(expand(st.Command, vars)), cached, "")
	if err != nil {
		return "", fmt.Errorf("build: schema: %w", err)
	}
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		logf("schema stderr:\n%s", tail(stderr))
	}
	return cached, nil
}

// WithoutPG drops libpq's PG* variables, so nothing run against a
// throwaway cluster is pointed at another server by the caller's
// environment.
func WithoutPG(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PG") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

var pluginRef = regexp.MustCompile(`\{plugins\}[/\\]([^/\\]+)`)

// SchemaPluginsDigest identifies the bundled plugin code a schema command
// runs (the {plugins}/<dir> it names), so editing another plugin does not
// invalidate the schema cache.
func SchemaPluginsDigest(command []string, pluginsDir string) (string, error) {
	var dirs []string
	for _, arg := range command {
		for _, m := range pluginRef.FindAllStringSubmatch(arg, -1) {
			if !slices.Contains(dirs, m[1]) {
				dirs = append(dirs, m[1])
			}
		}
	}
	slices.Sort(dirs)
	h := sha256.New()
	fmt.Fprintf(h, "schema-plugins:%q\n", dirs)
	for _, d := range dirs {
		sum, err := DirDigest(filepath.Join(pluginsDir, d))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("build: schema plugin digest: %w", err)
		}
		fmt.Fprintf(h, "%s=%s\n", d, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
