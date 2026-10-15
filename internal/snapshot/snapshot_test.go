package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/store"
)

// fakeGitHub implements the handful of release endpoints we use.
type fakeGitHub struct {
	mu       sync.Mutex
	srv      *httptest.Server
	releases map[int64]*Release
	assets   map[int64][]byte
	next     int64
	deleted  []string
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{releases: map[int64]*Release{}, assets: map[int64][]byte{}, next: 1}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *GitHub {
	return &GitHub{API: f.srv.URL, Uploads: f.srv.URL, Token: "t"}
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer t" {
		http.Error(w, "no auth", 401)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == "GET" && strings.HasSuffix(p, "/releases"):
		var out []Release
		for _, rel := range f.releases {
			out = append(out, *rel)
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && strings.HasSuffix(p, "/releases"):
		var in struct {
			Tag string `json:"tag_name"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		rel := &Release{ID: f.next, Tag: in.Tag}
		f.next++
		f.releases[rel.ID] = rel
		json.NewEncoder(w).Encode(rel)
	case r.Method == "POST" && strings.HasSuffix(p, "/assets"):
		var id int64
		fmt.Sscanf(p[strings.Index(p, "/releases/")+len("/releases/"):], "%d", &id)
		b, _ := io.ReadAll(r.Body)
		aid := f.next
		f.next++
		f.assets[aid] = b
		f.releases[id].Assets = append(f.releases[id].Assets, Asset{ID: aid, Name: r.URL.Query().Get("name"), URL: fmt.Sprintf("%s/assets/%d", f.srv.URL, aid)})
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	case r.Method == "GET" && strings.HasPrefix(p, "/assets/"):
		var aid int64
		fmt.Sscanf(strings.TrimPrefix(p, "/assets/"), "%d", &aid)
		w.Write(f.assets[aid])
	case r.Method == "DELETE" && strings.Contains(p, "/releases/"):
		var id int64
		fmt.Sscanf(p[strings.LastIndex(p, "/")+1:], "%d", &id)
		f.deleted = append(f.deleted, f.releases[id].Tag)
		delete(f.releases, id)
		w.WriteHeader(204)
	case r.Method == "DELETE" && strings.Contains(p, "/git/refs/tags/"):
		w.WriteHeader(204)
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, 500)
	}
}

func packFixture(t *testing.T, id string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := "../../testdata/workspace-mini/snapshot"
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			os.MkdirAll(filepath.Join(dir, rel), 0o755)
			return nil
		}
		b, _ := os.ReadFile(p)
		if rel == "manifest.json" {
			b = bytes.Replace(b, []byte("snapshot-20261015T120000Z"), []byte(id), 1)
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	var buf bytes.Buffer
	if err := bundle.Pack(dir, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPublishPullPrune(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	for _, id := range []string{"snapshot-20261015T100000Z", "snapshot-20261015T110000Z", "snapshot-20261015T120000Z"} {
		if _, err := gh.Publish(ctx, "o/map", id, "notes", packFixture(t, id)); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := store.Open(":memory:")
	defer s.Close()
	p := &Puller{Store: s, GitHub: gh, DataDir: t.TempDir()}
	if !p.Due("o/map") {
		t.Fatal("first check should be due")
	}
	res, err := p.Pull(ctx, "o/map")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported || res.Snapshot != "snapshot-20261015T120000Z" {
		t.Errorf("pull = %+v", res)
	}
	if p.Due("o/map") {
		t.Error("should not be due right after a pull")
	}
	if _, err := os.Stat(filepath.Join(p.DataDir, "snapshots", res.Snapshot, "manifest.json")); err != nil {
		t.Error("unpacked snapshot missing")
	}
	again, _ := p.Pull(ctx, "o/map")
	if again.Imported {
		t.Error("second pull should be a no-op")
	}
	n, err := gh.Prune(ctx, "o/map", 1)
	if err != nil || n != 2 {
		t.Errorf("prune = %d, %v", n, err)
	}
	rs, _ := gh.Snapshots(ctx, "o/map")
	if len(rs) != 1 || rs[0].Tag != "snapshot-20261015T120000Z" {
		t.Errorf("after prune: %+v", rs)
	}
}

func TestPullErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	s, _ := store.Open(":memory:")
	defer s.Close()
	p := &Puller{Store: s, GitHub: f.client(), DataDir: t.TempDir()}
	if _, err := p.Pull(ctx, "o/empty"); err == nil || !strings.Contains(err.Error(), "no snapshot releases") {
		t.Errorf("empty: %v", err)
	}
	bad := &GitHub{API: f.srv.URL, Uploads: f.srv.URL, Token: "wrong"}
	if _, err := bad.Snapshots(ctx, "o/map"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("auth hint missing: %v", err)
	}
	// A release whose asset holds a different snapshot id is rejected.
	gh := f.client()
	gh.Publish(ctx, "o/x", "snapshot-20261016T000000Z", "", packFixture(t, "snapshot-20261015T000000Z"))
	if _, err := p.Pull(ctx, "o/x"); err == nil || !strings.Contains(err.Error(), "holds snapshot") {
		t.Errorf("mismatch: %v", err)
	}
}
