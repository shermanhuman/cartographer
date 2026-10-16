// Package rebuild keeps a snapshot fresh from local checkouts: it finds the
// local workspace a snapshot came from, decides when a checkout ahead of the
// snapshot warrants a rebuild, and serializes rebuilds across processes with
// a lock file, a stamp and a log in the data directory.
package rebuild

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Stale is the lock age after which the rebuild holding it is presumed dead
// (a build killed with its session) and the lock is taken over.
const Stale = 30 * time.Minute

// FailBackoff is the minimum time before another rebuild after a failed
// one, so a broken plugin isn't rerun on every query.
const FailBackoff = 5 * time.Minute

// Timeout bounds a background rebuild (MCP server, serve).
const Timeout = 15 * time.Minute

// ErrRunning means another rebuild of the workspace holds the lock.
var ErrRunning = errors.New("a rebuild is already running")

// FindWorkspace returns the directory of the local workspace whose
// cartographer.toml names ws: the first configured directory (user config
// local_workspaces) that does, else a sibling of the current checkout. ""
// when there is none. Only the name key is read, so a workspace that
// fails validation is still found (the build then reports why).
func FindWorkspace(ws string, cur fresh.Checkout, configured []string) string {
	for _, dir := range configured {
		if dir = fresh.ExpandHome(dir); workspace.Name(dir) == ws {
			return dir
		}
	}
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
// file while a rebuild runs, a stamp of the last outcome, the last error
// and the last build's log.
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

// Lock takes ws's lock: an exclusively created file holding the pid and
// start time. A lock older than Stale is removed first. ErrRunning when
// another rebuild holds it.
func (d Dir) Lock(ws string) (unlock func(), err error) {
	path := d.lockPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, running := d.Running(ws); !running {
		os.Remove(path) // stale or absent
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrRunning
	}
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	f.Close()
	return func() { os.Remove(path) }, nil
}

// Running reports whether a rebuild of ws holds the lock, and since when.
func (d Dir) Running(ws string) (since time.Time, ok bool) {
	fi, err := os.Stat(d.lockPath(ws))
	if err != nil || time.Since(fi.ModTime()) > Stale {
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

// Run performs one rebuild under ws's lock and stamps the outcome; a
// failure's message is kept for Status until the next success. ErrRunning
// (nothing built or stamped) when the lock is held.
func (d Dir) Run(ws string, build func() error) error {
	unlock, err := d.Lock(ws)
	if err != nil {
		return err
	}
	defer unlock()
	err = build()
	d.stamp(ws, err)
	return err
}

func (d Dir) stamp(ws string, err error) {
	status := "ok"
	if err != nil {
		status = "failed"
		os.WriteFile(d.errorPath(ws), []byte(err.Error()+"\n"), 0o644)
	} else {
		os.Remove(d.errorPath(ws))
	}
	os.WriteFile(d.stampPath(ws), []byte(status+" "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
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
		first = first[:160] + "…"
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
