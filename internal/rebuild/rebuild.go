// Package rebuild keeps a snapshot fresh from local checkouts: it finds the
// local workspace a snapshot came from, decides when a checkout ahead of the
// snapshot warrants a rebuild, and serializes rebuilds across processes with
// a lock file, a stamp and a log in the data directory.
package rebuild

import (
	"bytes"
	"context"
	"crypto/rand"
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

// Timeout bounds a background rebuild (MCP server, serve).
const Timeout = 15 * time.Minute

// ErrRunning means another rebuild of the workspace holds the lock.
var ErrRunning = errors.New("a rebuild is already running")

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

// Dir is the data directory holding rebuild state: per workspace a lock
// file while a rebuild runs, a stamp of the last outcome (with the
// revisions a successful one built), the last error and the last log.
type Dir struct{ Path string }

func (d Dir) file(ws, ext string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, ws)
	return filepath.Join(d.Path, "rebuild", safe+ext)
}

func (d Dir) lockPath(ws string) string  { return d.file(ws, ".lock") }
func (d Dir) stampPath(ws string) string { return d.file(ws, ".stamp") }
func (d Dir) errorPath(ws string) string { return d.file(ws, ".error") }

// LogPath is where a rebuild of ws writes its output.
func (d Dir) LogPath(ws string) string { return d.file(ws, ".log") }

// Lock takes ws's lock: an exclusively created file holding the pid, the
// start time and a token. A stale lock (see Running) is taken over under a
// short-lived takeover lock, so of several processes finding it stale only
// one proceeds. ErrRunning when another rebuild holds it.
func (d Dir) Lock(ws string) (unlock func(), err error) {
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
			return func() {
				// Only our own lock: after a takeover it is another's.
				if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, content) {
					os.Remove(path)
				}
			}, nil
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
	if fi, err := os.Stat(guard); err == nil && time.Since(fi.ModTime()) > time.Minute {
		os.Remove(guard) // left by a process that died mid-takeover
	}
	g, err := os.OpenFile(guard, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false
	}
	g.Close()
	defer os.Remove(guard)
	seen, err := os.ReadFile(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist) // released meanwhile: try again
	}
	if !stale(path, seen) {
		return false
	}
	tok := make([]byte, 8)
	rand.Read(tok)
	aside := path + ".stale-" + hex.EncodeToString(tok)
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

// stale reports whether the lock at path with the given content belongs to
// no running rebuild: its process is gone, or it is older than Stale.
func stale(path string, content []byte) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	if time.Since(fi.ModTime()) > Stale {
		return true
	}
	pidField, _, _ := strings.Cut(string(content), " ")
	pid, err := strconv.Atoi(pidField)
	return err == nil && !alive(pid)
}

// Running reports whether a rebuild of ws holds the lock, and since when.
func (d Dir) Running(ws string) (since time.Time, ok bool) {
	path := d.lockPath(ws)
	b, err := os.ReadFile(path)
	if err != nil || stale(path, b) {
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

// Built returns the revisions (repo -> commit) the last rebuild built when
// it succeeded, else nil.
func (d Dir) Built(ws string) map[string]string {
	b, err := os.ReadFile(d.stampPath(ws))
	if err != nil || !strings.HasPrefix(string(b), "ok") {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")[1:]
	revs := map[string]string{}
	for _, l := range lines {
		if name, rev, ok := strings.Cut(l, " "); ok {
			revs[name] = rev
		}
	}
	return revs
}

// Run performs one rebuild under ws's lock and stamps the outcome: the
// revisions built on success, the error (kept for Status until the next
// success) on failure. Everything the rebuild does, its setup included,
// belongs in build, so any failure backs off. A cancelled build (shutdown)
// is not stamped. ErrRunning (nothing built or stamped) when the lock is
// held.
func (d Dir) Run(ws string, build func() (map[string]string, error)) error {
	unlock, err := d.Lock(ws)
	if err != nil {
		return err
	}
	defer unlock()
	revs, err := build()
	if !errors.Is(err, context.Canceled) {
		d.stamp(ws, revs, err)
	}
	return err
}

// Fail records a rebuild that could not start (e.g. the child process), so
// it backs off and Status reports it like a failed build.
func (d Dir) Fail(ws string, err error) {
	os.MkdirAll(filepath.Dir(d.stampPath(ws)), 0o755)
	d.stamp(ws, nil, err)
}

func (d Dir) stamp(ws string, revs map[string]string, err error) {
	var b strings.Builder
	status := "ok"
	if err != nil {
		status = "failed"
		os.WriteFile(d.errorPath(ws), []byte(err.Error()+"\n"), 0o644)
	} else {
		os.Remove(d.errorPath(ws))
	}
	fmt.Fprintf(&b, "%s %s\n", status, time.Now().UTC().Format(time.RFC3339))
	names := make([]string, 0, len(revs))
	for name := range revs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s %s\n", name, revs[name])
	}
	os.WriteFile(d.stampPath(ws), []byte(b.String()), 0o644)
}

// Scratch returns a new scratch directory for a rebuild of ws, first
// removing any left by a rebuild that died. The caller holds the lock.
func (d Dir) Scratch(ws string) (string, error) {
	base := d.file(ws, ".build-")
	old, _ := filepath.Glob(base + "*")
	for _, dir := range old {
		os.RemoveAll(dir)
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(filepath.Dir(base), filepath.Base(base)+"*")
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
