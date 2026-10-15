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
func (p *Puller) Pull(ctx context.Context, source string) (*Result, error) {
	rs, err := p.GitHub.Snapshots(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("%s: %w", source, ErrNoReleases)
	}
	newest := rs[0]
	p.stamp(source)
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
	dir := filepath.Join(p.DataDir, "snapshots", newest.Tag)
	tmp := dir + ".partial"
	os.RemoveAll(tmp)
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
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
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

// Due reports whether source hasn't been checked within AutoInterval.
func (p *Puller) Due(source string) bool {
	fi, err := os.Stat(p.stampPath(source))
	return err != nil || time.Since(fi.ModTime()) > AutoInterval
}

func (p *Puller) stampPath(source string) string {
	return filepath.Join(p.DataDir, "stamps", strings.ReplaceAll(source, "/", "__"))
}

func (p *Puller) stamp(source string) {
	path := p.stampPath(source)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
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
		if !keep[e.Name()] {
			os.RemoveAll(filepath.Join(p.DataDir, "snapshots", e.Name()))
		}
	}
}
