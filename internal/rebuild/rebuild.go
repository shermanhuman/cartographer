// Package rebuild keeps a snapshot fresh from local checkouts: it finds the
// local workspace a snapshot came from, decides when a checkout ahead of the
// snapshot warrants a rebuild, and serializes rebuilds across processes with
// a lock file, a stamp and a log in the data directory.
package rebuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Stale is the lock age after which the rebuild holding it is presumed dead
// even if its pid is alive (pid reuse; platforms without a liveness check).
const Stale = 30 * time.Minute

// FailBackoff is the minimum time before another rebuild after a failed
// one, so a broken plugin isn't rerun on every query.
const FailBackoff = 5 * time.Minute

// Timeout bounds an automatic rebuild (the detached child, or the MCP
// server's and serve's goroutine).
var Timeout = 15 * time.Minute

// Heartbeat is how often a running rebuild refreshes its lock, so a live
// build never ages out (Stale) however long it runs.
var Heartbeat = time.Minute

// ErrRunning means another rebuild of the workspace holds the lock.
var ErrRunning = errors.New("a rebuild is already running")

// ErrLost means a rebuild's lock was taken over while it ran (it outlived
// Stale without a heartbeat); its outcome is not recorded.
var ErrLost = errors.New("another rebuild took over the lock")

// Result is what a successful rebuild built: the revisions (repo ->
// commit) and the store they were imported into ("" when not imported).
// Only an import counts as built (see Built).
type Result struct {
	DB        string
	Revisions map[string]string
}

// Registered returns the first configured directory (user config
// local_workspaces, recorded by `workspace add <dir>`) whose cartographer.toml
// names ws, or "". Only registered workspaces are rebuilt automatically:
// a rebuild runs the workspace's plugin commands, and registering is the
// user's decision to trust them.
func Registered(ws string, configured []string) string {
	for _, dir := range configured {
		if dir = filepath.Clean(fresh.ExpandHome(dir)); workspace.Name(dir) == ws {
			return dir
		}
	}
	return ""
}

// Sibling returns a directory beside the current checkout whose
// cartographer.toml names ws, or "": a workspace to suggest registering,
// never one to run. Only the name key is read.
func Sibling(ws string, cur fresh.Checkout) string {
	if cur.Root == "" {
		return ""
	}
	parent := filepath.Dir(cur.Root)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		dir := filepath.Join(parent, e.Name())
		if !e.IsDir() || dir == cur.Root {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, workspace.FileName)); err != nil {
			continue
		}
		if workspace.Name(dir) == ws {
			return dir
		}
	}
	return ""
}

// Needed reports whether a checkout is ahead of the snapshot: commits past
// the snapshot revision, or a revision the clone doesn't have at all.
// Uncommitted edits alone don't count; they stay marked ⚠ until committed.
func Needed(rep *fresh.Report) bool {
	if rep == nil {
		return false
	}
	for _, st := range rep.Repos {
		if st.Behind > 0 || st.Note == fresh.NoteRevisionMissing {
			return true
		}
	}
	return false
}

// Dir is the data directory holding rebuild state: per workspace, in its
// own directory (StateDir), a lock file while a rebuild runs, a stamp of
// the last outcome (with what a successful one imported), the last error,
// the last log and the running build's scratch.
type Dir struct{ Path string }

// StateDir is ws's state directory: the name made file-safe plus a hash of
// the exact name, so distinct names ("a/b", "a_b") never share one.
func (d Dir) StateDir(ws string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, ws)
	sum := sha256.Sum256([]byte(ws))
	return filepath.Join(d.Path, "rebuild", safe+"-"+hex.EncodeToString(sum[:4]))
}

func (d Dir) lockPath(ws string) string  { return filepath.Join(d.StateDir(ws), "lock") }
func (d Dir) stampPath(ws string) string { return filepath.Join(d.StateDir(ws), "stamp") }
func (d Dir) errorPath(ws string) string { return filepath.Join(d.StateDir(ws), "error") }

// LogPath is where a rebuild of ws writes its output.
func (d Dir) LogPath(ws string) string { return filepath.Join(d.StateDir(ws), "log") }

// StampPath is the record of ws's last rebuild outcome.
func (d Dir) StampPath(ws string) string { return d.stampPath(ws) }

// Lock takes ws's lock: an exclusively created file holding the pid, the
// start time and a token. A stale lock (see Running) is taken over under a
// short-lived takeover lock, so of several processes finding it stale only
// one proceeds. ErrRunning when another rebuild holds it.
func (d Dir) Lock(ws string) (unlock func(), err error) {
	h, err := d.lock(ws)
	if err != nil {
		return nil, err
	}
	return h.unlock, nil
}

// held is a lock this process took.
type held struct {
	path    string
	content []byte
}

// owned reports whether the lock is still ours (not taken over).
func (h *held) owned() bool {
	b, err := os.ReadFile(h.path)
	return err == nil && bytes.Equal(b, h.content)
}

// touch refreshes the lock's mtime while it is ours.
func (h *held) touch() {
	if h.owned() {
		now := time.Now()
		os.Chtimes(h.path, now, now)
	}
}

func (h *held) unlock() {
	if h.owned() { // after a takeover it is another's
		os.Remove(h.path)
	}
}

func (d Dir) lock(ws string) (*held, error) {
	path := d.lockPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tok := make([]byte, 8)
	rand.Read(tok)
	content := []byte(fmt.Sprintf("%d %s %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), hex.EncodeToString(tok)))
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, err = f.Write(content)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(path)
				return nil, err
			}
			return &held{path, content}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if attempt > 0 || !d.takeOver(ws) {
			return nil, ErrRunning
		}
	}
}

// takeOver removes ws's lock if it is stale, holding the takeover lock
// while it checks and removes, and reports whether it did. The lock is
// moved aside and compared with what was judged stale; a different (fresh)
// lock moved by mistake is linked back.
func (d Dir) takeOver(ws string) bool {
	path := d.lockPath(ws)
	guard := path + ".takeover"
	if b, err := os.ReadFile(guard); err == nil && stale(guard, b, time.Minute) {
		os.Remove(guard) // left by a process that died mid-takeover
	}
	g, err := os.OpenFile(guard, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false
	}
	fmt.Fprintf(g, "%d\n", os.Getpid())
	g.Close()
	defer os.Remove(guard)
	seen, err := os.ReadFile(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist) // released meanwhile: try again
	}
	if !stale(path, seen, Stale) {
		return false
	}
	tok := make([]byte, 8)
	rand.Read(tok)
	aside := path + ".stale-" + hex.EncodeToString(tok) // swept by Scratch if we die here
	if err := os.Rename(path, aside); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	defer os.Remove(aside)
	if moved, err := os.ReadFile(aside); err != nil || !bytes.Equal(moved, seen) {
		os.Link(aside, path) // fails if yet another lock exists; either way we lost
		return false
	}
	return true
}

// stale reports whether the lock (or guard) at path with the given content
// (starting with a pid) belongs to no live process: its process is gone,
// or it is older than maxAge.
func stale(path string, content []byte, maxAge time.Duration) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	if time.Since(fi.ModTime()) > maxAge {
		return true
	}
	pidField, _, _ := strings.Cut(strings.TrimSpace(string(content)), " ")
	pid, err := strconv.Atoi(pidField)
	return err == nil && !alive(pid)
}

// Running reports whether a rebuild of ws holds the lock, and since when.
func (d Dir) Running(ws string) (since time.Time, ok bool) {
	path := d.lockPath(ws)
	b, err := os.ReadFile(path)
	if err != nil || stale(path, b, Stale) {
		return time.Time{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// Due reports whether ws may be rebuilt: not within FailBackoff of a failed
// rebuild. A successful one never backs off; the next evidence of a
// checkout ahead of the snapshot rebuilds again.
func (d Dir) Due(ws string) bool {
	path := d.stampPath(ws)
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(b), "failed") {
		return true
	}
	return time.Since(fi.ModTime()) > FailBackoff
}

// Built returns the revisions the last rebuild imported into the store at
// db when it succeeded, and when that was; nil when it didn't (a build
// not imported there changed nothing a query from that store sees).
func (d Dir) Built(ws, db string) (map[string]string, time.Time) {
	b, err := os.ReadFile(d.stampPath(ws))
	if err != nil || !strings.HasPrefix(string(b), "ok") || db == "" {
		return nil, time.Time{}
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")[1:]
	if len(lines) == 0 || lines[0] != "db "+filepath.Clean(db) {
		return nil, time.Time{}
	}
	revs := map[string]string{}
	for _, l := range lines[1:] {
		if name, rev, ok := strings.Cut(l, " "); ok {
			revs[name] = rev
		}
	}
	var at time.Time
	if fi, err := os.Stat(d.stampPath(ws)); err == nil {
		at = fi.ModTime()
	}
	return revs, at
}

// Run performs one rebuild under ws's lock, refreshing it every Heartbeat,
// and stamps the outcome: what was imported on success, the error (kept
// for Status until the next success) on failure. Everything the rebuild
// does, its setup included, belongs in build, so any failure backs off. A
// cancelled build (shutdown) is not stamped, nor one whose lock was taken
// over meanwhile (ErrLost). ErrRunning (nothing built or stamped) when the
// lock is held.
func (d Dir) Run(ws string, build func() (Result, error)) error {
	h, err := d.lock(ws)
	if err != nil {
		return err
	}
	defer h.unlock()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				h.touch()
			}
		}
	}()
	res, err := build()
	close(stop)
	<-done
	if !h.owned() {
		if err != nil {
			return fmt.Errorf("%w (its build: %v)", ErrLost, err)
		}
		return ErrLost
	}
	if !errors.Is(err, context.Canceled) {
		d.stamp(ws, res, err)
	}
	return err
}

// Fail records a rebuild that could not start (e.g. the child process), so
// it backs off and Status reports it like a failed build.
func (d Dir) Fail(ws string, err error) {
	os.MkdirAll(d.StateDir(ws), 0o755)
	d.stamp(ws, Result{}, err)
}

func (d Dir) stamp(ws string, res Result, err error) {
	var b strings.Builder
	status := "ok"
	if err != nil {
		status = "failed"
		os.WriteFile(d.errorPath(ws), []byte(err.Error()+"\n"), 0o644)
	} else {
		os.Remove(d.errorPath(ws))
	}
	fmt.Fprintf(&b, "%s %s\n", status, time.Now().UTC().Format(time.RFC3339))
	if err == nil && res.DB != "" {
		fmt.Fprintf(&b, "db %s\n", filepath.Clean(res.DB))
		names := make([]string, 0, len(res.Revisions))
		for name := range res.Revisions {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "%s %s\n", name, res.Revisions[name])
		}
	}
	os.WriteFile(d.stampPath(ws), []byte(b.String()), 0o644)
}

// Scratch returns a new scratch directory for a rebuild of ws in its
// state directory, first removing what a rebuild or takeover that died
// left there (scratch, moved-aside locks). The caller holds the lock.
func (d Dir) Scratch(ws string) (string, error) {
	dir := d.StateDir(ws)
	old, _ := filepath.Glob(filepath.Join(dir, "build-*"))
	aside, _ := filepath.Glob(filepath.Join(dir, "lock.stale-*"))
	for _, p := range append(old, aside...) {
		os.RemoveAll(p)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, "build-")
}

// Status is a one-line note for headers and status: "rebuilding since …"
// while a rebuild holds the lock, "last rebuild failed …" after a failure
// (until a rebuild succeeds), else "".
func (d Dir) Status(ws string) string {
	if since, ok := d.Running(ws); ok {
		return "rebuilding since " + clock(since)
	}
	b, err := os.ReadFile(d.errorPath(ws))
	if err != nil {
		return ""
	}
	fi, _ := os.Stat(d.errorPath(ws))
	first, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if len(first) > 160 {
		cut := 160
		for cut > 0 && !utf8.RuneStart(first[cut]) {
			cut--
		}
		first = first[:cut] + "…"
	}
	s := "last rebuild failed"
	if fi != nil {
		s += " " + clock(fi.ModTime())
	}
	s += ": " + first
	if _, err := os.Stat(d.LogPath(ws)); err == nil {
		s += " (log: " + d.LogPath(ws) + ")"
	}
	return s
}

// clock formats t for a human: the time today, else the date too.
func clock(t time.Time) string {
	t = t.Local()
	if t.Format(time.DateOnly) == time.Now().Format(time.DateOnly) {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
