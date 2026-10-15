package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/store"
)

// AutoInterval is the minimum time between automatic pull checks per source.
const AutoInterval = time.Hour

// FailBackoff is the minimum time before retrying a source whose last check
// failed (e.g. offline), so a failing source isn't retried on every call.
const FailBackoff = 5 * time.Minute

// Puller fetches published snapshots into the store.
type Puller struct {
	Store   *store.Store
	GitHub  *GitHub
	DataDir string // unpacked snapshots and pull stamps live here
	Keep    int    // snapshots kept in the store (default store.DefaultKeep)
}

// Result says what a pull did.
type Result struct {
	Source   string
	Snapshot string
	Imported bool // false when already up to date
}

// Pull imports the newest snapshot release of source (owner/name) if the
// store doesn't have it yet.
// Every attempt is stamped, failed ones included, so Due backs off.
func (p *Puller) Pull(ctx context.Context, source string) (res *Result, err error) {
	defer func() { p.stamp(source, err == nil) }()
	rs, err := p.GitHub.Snapshots(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("%s: %w", source, ErrNoReleases)
	}
	newest := rs[0]
	have, err := p.Store.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range have {
		if s.ID == newest.Tag {
			return &Result{Source: source, Snapshot: newest.Tag}, nil
		}
	}
	body, err := p.GitHub.Download(ctx, newest)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	snaps := filepath.Join(p.DataDir, "snapshots")
	dir := filepath.Join(snaps, newest.Tag)
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return nil, err
	}
	// A unique temp dir per pull: concurrent pulls never share one, and
	// pruneDirs leaves anything marked partial alone.
	tmp, err := os.MkdirTemp(snaps, newest.Tag+partialMark+"*")
	if err != nil {
		return nil, err
	}
	if err := bundle.Unpack(body, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("%s: unpack: %w", newest.Tag, err)
	}
	b, err := bundle.Read(tmp)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if b.Manifest.ID != newest.Tag {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("release %s holds snapshot %s", newest.Tag, b.Manifest.ID)
	}
	if err := p.Store.ImportBundle(ctx, b); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent pull of the same snapshot may have renamed first.
		os.RemoveAll(tmp)
		if _, serr := os.Stat(filepath.Join(dir, "manifest.json")); serr != nil {
			return nil, err
		}
	}
	keep := p.Keep
	if keep == 0 {
		keep = store.DefaultKeep
	}
	if err := p.Store.Prune(ctx, keep); err != nil {
		return nil, err
	}
	p.pruneDirs(ctx)
	return &Result{Source: source, Snapshot: newest.Tag, Imported: true}, nil
}

// partialMark names in-progress unpack directories.
const partialMark = ".partial-"

// Due reports whether source hasn't been checked within AutoInterval, or
// within FailBackoff when the last check failed.
func (p *Puller) Due(source string) bool {
	path := p.stampPath(source)
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	interval := AutoInterval
	if b, err := os.ReadFile(path); err == nil && strings.HasPrefix(string(b), "failed") {
		interval = FailBackoff
	}
	return time.Since(fi.ModTime()) > interval
}

func (p *Puller) stampPath(source string) string {
	return filepath.Join(p.DataDir, "stamps", strings.ReplaceAll(source, "/", "__"))
}

func (p *Puller) stamp(source string, ok bool) {
	path := p.stampPath(source)
	os.MkdirAll(filepath.Dir(path), 0o755)
	status := "ok"
	if !ok {
		status = "failed"
	}
	os.WriteFile(path, []byte(status+" "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// pruneDirs removes unpacked snapshot directories no longer in the store.
func (p *Puller) pruneDirs(ctx context.Context) {
	have, err := p.Store.Snapshots(ctx)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, s := range have {
		keep[s.ID] = true
	}
	entries, _ := os.ReadDir(filepath.Join(p.DataDir, "snapshots"))
	for _, e := range entries {
		if !keep[e.Name()] && !strings.Contains(e.Name(), partialMark) && !strings.HasSuffix(e.Name(), ".partial") {
			os.RemoveAll(filepath.Join(p.DataDir, "snapshots", e.Name()))
		}
	}
}
