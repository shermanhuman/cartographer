package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
	calls    []string // release write calls, in order
	// failUpload / failPublish make the asset upload / the draft's publish
	// PATCH fail.
	failUpload, failPublish bool
	// failDelete makes release DELETEs fail; refStatus, when set, is the
	// status of tag ref DELETEs.
	failDelete bool
	refStatus  int
	// onUpload runs inside the upload handler.
	onUpload func()
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
			Tag   string `json:"tag_name"`
			Draft bool   `json:"draft"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.calls = append(f.calls, fmt.Sprintf("create draft=%v", in.Draft))
		rel := &Release{ID: f.next, Tag: in.Tag, Draft: in.Draft}
		f.next++
		f.releases[rel.ID] = rel
		json.NewEncoder(w).Encode(rel)
	case r.Method == "PATCH" && strings.Contains(p, "/releases/"):
		var id int64
		fmt.Sscanf(p[strings.LastIndex(p, "/")+1:], "%d", &id)
		var in struct {
			Draft      *bool  `json:"draft"`
			MakeLatest string `json:"make_latest"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.calls = append(f.calls, fmt.Sprintf("update draft=%v latest=%s", in.Draft != nil && *in.Draft, in.MakeLatest))
		if f.failPublish {
			http.Error(w, "boom", 500)
			return
		}
		rel := f.releases[id]
		if in.Draft != nil {
			rel.Draft = *in.Draft
		}
		json.NewEncoder(w).Encode(rel)
	case r.Method == "POST" && strings.HasSuffix(p, "/assets"):
		var id int64
		fmt.Sscanf(p[strings.Index(p, "/releases/")+len("/releases/"):], "%d", &id)
		f.calls = append(f.calls, "upload")
		if f.onUpload != nil {
			f.onUpload()
		}
		if f.failUpload {
			http.Error(w, "boom", 500)
			return
		}
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
		f.calls = append(f.calls, "delete")
		if f.failDelete {
			http.Error(w, "boom", 500)
			return
		}
		if f.releases[id] == nil {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		f.deleted = append(f.deleted, f.releases[id].Tag)
		delete(f.releases, id)
		w.WriteHeader(204)
	case r.Method == "DELETE" && strings.Contains(p, "/git/refs/tags/"):
		if f.refStatus != 0 {
			http.Error(w, "ref", f.refStatus)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, 500)
	}
}

// add stores a release directly, with the snapshot asset when withAsset.
func (f *fakeGitHub) add(t *testing.T, tag string, draft, withAsset bool) int64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	rel := &Release{ID: f.next, Tag: tag, Draft: draft}
	f.next++
	if withAsset {
		aid := f.next
		f.next++
		f.assets[aid] = packFixture(t, tag)
		rel.Assets = []Asset{{ID: aid, Name: AssetName, URL: fmt.Sprintf("%s/assets/%d", f.srv.URL, aid)}}
	}
	f.releases[rel.ID] = rel
	return rel.ID
}

func (f *fakeGitHub) tags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.releases {
		out = append(out, r.Tag)
	}
	sort.Strings(out)
	return out
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
	if entries, _ := os.ReadDir(filepath.Join(p.DataDir, "snapshots")); len(entries) != 0 {
		t.Errorf("unpacked snapshot kept after import: %v", entries)
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

// A stored snapshot of another workspace that happens to share the release's
// id is not "already present".
func TestPullChecksStoredWorkspace(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	id := bundle.SnapshotID("mini", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if _, err := gh.Publish(ctx, "o/map", id, "notes", packFixture(t, id)); err != nil {
		t.Fatal(err)
	}
	s, _ := store.Open(":memory:")
	defer s.Close()
	other := &bundle.Bundle{Manifest: bundle.Manifest{SnapshotFormat: bundle.Format, ID: id, Workspace: "other", CreatedAt: "2026-10-15T12:00:00Z"}}
	if err := s.ImportBundle(ctx, other); err != nil {
		t.Fatal(err)
	}
	p := &Puller{Store: s, GitHub: gh, DataDir: t.TempDir()}
	res, err := p.Pull(ctx, "o/map")
	if err == nil {
		t.Fatalf("pull skipped or replaced another workspace's snapshot: %+v", res)
	}
	if all, _ := s.Snapshots(ctx); len(all) != 1 || all[0].Workspace != "other" {
		t.Errorf("snapshots = %+v", all)
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

func TestHangingServerTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	gh := &GitHub{API: srv.URL, Token: "t", Timeout: 100 * time.Millisecond}
	start := time.Now()
	if _, err := gh.Snapshots(context.Background(), "o/map"); err == nil {
		t.Fatal("hanging server should time out")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout took %v", d)
	}
}

func TestFailedPullBacksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", 503)
	}))
	defer srv.Close()
	s, _ := store.Open(":memory:")
	defer s.Close()
	p := &Puller{Store: s, GitHub: &GitHub{API: srv.URL, Token: "t"}, DataDir: t.TempDir()}
	if !p.Due("o/map") {
		t.Fatal("first check should be due")
	}
	if _, err := p.Pull(context.Background(), "o/map"); err == nil {
		t.Fatal("pull from a failing server should fail")
	}
	if p.Due("o/map") {
		t.Error("a failed attempt must be stamped so the next call doesn't retry")
	}
	// After FailBackoff (not the full AutoInterval) it is due again.
	old := time.Now().Add(-FailBackoff - time.Minute)
	os.Chtimes(p.stampPath("o/map"), old, old)
	if !p.Due("o/map") {
		t.Error("failed source should be due after FailBackoff")
	}
}

func TestPruneRejectsKeepBelowOne(t *testing.T) {
	f := newFake(t)
	for _, keep := range []int{0, -1} {
		if _, err := f.client().Prune(context.Background(), "o/map", keep); err == nil {
			t.Errorf("keep=%d should fail", keep)
		}
	}
}

func TestConcurrentPulls(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	id := "snapshot-20261015T120000Z"
	if _, err := gh.Publish(ctx, "o/map", id, "", packFixture(t, id)); err != nil {
		t.Fatal(err)
	}
	s, _ := store.Open(":memory:")
	defer s.Close()
	data := t.TempDir()
	// Another process's in-progress pull must survive pruning; a snapshot
	// left unpacked by an older version is removed.
	busy := filepath.Join(data, "snapshots", "snapshot-x"+partialMark+"123")
	os.MkdirAll(busy, 0o755)
	os.MkdirAll(filepath.Join(data, "snapshots", "snapshot-20261001T000000Z"), 0o755)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &Puller{Store: s, GitHub: gh, DataDir: data}
			_, errs[i] = p.Pull(ctx, "o/map")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("pull %d: %v", i, err)
		}
	}
	if got, err := s.Latest(ctx); err != nil || got != id {
		t.Errorf("Latest = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(data, "snapshots"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "snapshot-x"+partialMark+"123" {
		t.Errorf("snapshot dirs = %v", names)
	}
}

func TestPruneDirsRemovesOnlyStalePartials(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	p := &Puller{Store: s, DataDir: t.TempDir()}
	snaps := filepath.Join(p.DataDir, "snapshots")
	fresh := filepath.Join(snaps, "snapshot-x"+partialMark+"1")
	stale := filepath.Join(snaps, "snapshot-y"+partialMark+"2")
	for _, d := range []string{fresh, stale} {
		os.MkdirAll(d, 0o755)
	}
	old := time.Now().Add(-2 * StalePartial)
	mine := filepath.Join(snaps, "notes")
	os.MkdirAll(mine, 0o755)
	os.Chtimes(stale, old, old)
	p.pruneDirs()
	if _, err := os.Stat(fresh); err != nil {
		t.Error("an in-progress pull's directory was removed")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("an abandoned partial directory was kept")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("a directory cartographer didn't make was removed")
	}
}

// Publish creates the release as a draft, uploads the asset, then publishes
// it, so an interrupted run never leaves a published release without its
// asset. A failed upload or publish deletes the draft.
func TestPublishDraftFirst(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	id := "snapshot-20261015T120000Z"
	r, err := gh.Publish(ctx, "o/map", id, "notes", packFixture(t, id))
	if err != nil {
		t.Fatal(err)
	}
	want := "create draft=true,upload,update draft=false latest=true"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	if r.Draft || f.releases[r.ID].Draft {
		t.Error("release still a draft after publish")
	}

	for _, tc := range []struct {
		name  string
		setup func()
		want  string
	}{
		{"upload fails", func() { f.failUpload = true }, "create draft=true,upload,delete"},
		{"publish fails", func() { f.failUpload, f.failPublish = false, true }, "create draft=true,upload,update draft=false latest=true,delete"},
	} {
		f.calls, f.deleted = nil, nil
		tc.setup()
		bad := "snapshot-20261015T130000Z"
		if _, err := gh.Publish(ctx, "o/map", bad, "", packFixture(t, bad)); err == nil {
			t.Errorf("%s: publish should fail", tc.name)
		}
		if got := strings.Join(f.calls, ","); got != tc.want {
			t.Errorf("%s: calls = %s, want %s", tc.name, got, tc.want)
		}
		if got := strings.Join(f.tags(), ","); got != id {
			t.Errorf("%s: releases left = %s", tc.name, got)
		}
	}
}

// A newer draft or asset-less release (an interrupted publish) is never
// listed, so Pull imports the newest complete snapshot.
func TestSnapshotsSkipIncomplete(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	f.add(t, "snapshot-20261015T100000Z", false, true)
	f.add(t, "snapshot-20261015T110000Z", true, true)   // leftover draft
	f.add(t, "snapshot-20261015T120000Z", false, false) // published, no asset
	rs, err := gh.Snapshots(ctx, "o/map")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Tag != "snapshot-20261015T100000Z" {
		t.Fatalf("snapshots = %+v", rs)
	}
	s, _ := store.Open(":memory:")
	defer s.Close()
	p := &Puller{Store: s, GitHub: gh, DataDir: t.TempDir()}
	res, err := p.Pull(ctx, "o/map")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported || res.Snapshot != "snapshot-20261015T100000Z" {
		t.Errorf("pull = %+v", res)
	}

	// Only incomplete releases: nothing to pull.
	g := newFake(t)
	g.add(t, "snapshot-20261015T110000Z", true, true)
	g.add(t, "snapshot-20261015T120000Z", false, false)
	p.GitHub = g.client()
	if _, err := p.Pull(ctx, "o/map"); err == nil || !strings.Contains(err.Error(), "no snapshot releases") {
		t.Errorf("incomplete only: %v", err)
	}
}

// tagAgo is a snapshot tag stamped d ago.
func tagAgo(d time.Duration) string {
	return bundle.SnapshotID("mini", time.Now().Add(-d))
}

// Prune keeps the newest complete snapshots and deletes incomplete releases
// older than the newest complete one once their tag is older than
// StalePartial. Newer incomplete ones, and recent ones (an overlapping
// publish still in progress), are left alone; incomplete releases never
// count toward keep.
func TestPruneIncomplete(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	var (
		oldBare    = tagAgo(10 * time.Hour) // published, no asset
		c1         = tagAgo(9 * time.Hour)
		staleDraft = tagAgo(8 * time.Hour)
		busyDraft  = tagAgo(30 * time.Second) // overlapping run, still uploading
		c2         = tagAgo(10 * time.Second)
		newDraft   = tagAgo(0) // newer than every complete one
	)
	f.add(t, oldBare, false, false)
	f.add(t, c1, false, true)
	f.add(t, staleDraft, true, true)
	f.add(t, busyDraft, true, false)
	f.add(t, c2, false, true)
	f.add(t, newDraft, true, false)
	f.add(t, "notes-1", false, false) // not a snapshot
	tags := func(ts ...string) string { sort.Strings(ts); return strings.Join(ts, ",") }
	n, err := gh.Prune(ctx, "o/map", 2)
	if err != nil || n != 2 {
		t.Errorf("prune = %d, %v", n, err)
	}
	if got, want := strings.Join(f.tags(), ","), tags("notes-1", c1, busyDraft, c2, newDraft); got != want {
		t.Errorf("after prune keep=2:\n got %s\nwant %s", got, want)
	}
	n, err = gh.Prune(ctx, "o/map", 1)
	if err != nil || n != 1 {
		t.Errorf("prune = %d, %v", n, err)
	}
	if got, want := strings.Join(f.tags(), ","), tags("notes-1", busyDraft, c2, newDraft); got != want {
		t.Errorf("after prune keep=1:\n got %s\nwant %s", got, want)
	}

	// No complete snapshot yet: incomplete releases are left alone.
	g := newFake(t)
	g.add(t, tagAgo(5*time.Hour), true, false)
	if n, err := g.client().Prune(ctx, "o/map", 1); err != nil || n != 0 {
		t.Errorf("prune with nothing complete = %d, %v", n, err)
	}
}

// A failed cleanup is reported along with the error that triggered it, so
// the operator learns a draft was left behind.
func TestPublishReportsFailedCleanup(t *testing.T) {
	f := newFake(t)
	f.failUpload, f.failDelete = true, true
	id := "snapshot-20261015T120000Z"
	_, err := f.client().Publish(context.Background(), "o/map", id, "", packFixture(t, id))
	if err == nil || !strings.Contains(err.Error(), "/assets") || !strings.Contains(err.Error(), "delete draft "+id) {
		t.Errorf("err = %v", err)
	}
}

// The draft is deleted even when ctx is cancelled mid-publish.
func TestPublishCleansUpAfterCancel(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onUpload = cancel
	f.failUpload = true
	id := "snapshot-20261015T120000Z"
	if _, err := f.client().Publish(ctx, "o/map", id, "", packFixture(t, id)); err == nil {
		t.Fatal("publish should fail")
	}
	if got := f.tags(); len(got) != 0 {
		t.Errorf("draft left after cancel: %v", got)
	}
}

// deleteRelease tolerates only the statuses that mean "already gone": 404 on
// the release (a racing prune), 404/422 on the tag ref (a draft has no tag).
// A tag containing "422" must not make a 5xx look tolerated.
func TestDeleteReleaseStatuses(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	gh := f.client()
	tag := "snapshot-20261015T042200Z-a4221b00"
	if err := gh.deleteRelease(ctx, "o/map", Release{ID: 99, Tag: tag}); err != nil {
		t.Errorf("missing release: %v", err)
	}
	for _, tc := range []struct {
		status int
		ok     bool
	}{{404, true}, {422, true}, {500, false}, {502, false}} {
		f.refStatus = tc.status
		id := f.add(t, tag, false, false)
		err := gh.deleteRelease(ctx, "o/map", Release{ID: id, Tag: tag})
		if (err == nil) != tc.ok {
			t.Errorf("ref DELETE %d: err = %v", tc.status, err)
		}
		var he *HTTPError
		if err != nil && (!errors.As(err, &he) || he.StatusCode != tc.status) {
			t.Errorf("ref DELETE %d: want *HTTPError, got %v", tc.status, err)
		}
	}
	f.refStatus, f.failDelete = 0, true
	if err := gh.deleteRelease(ctx, "o/map", Release{ID: 1, Tag: tag}); err == nil {
		t.Error("release DELETE 500 swallowed")
	}
}
