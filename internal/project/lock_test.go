package project_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
)

// pauseFetch redirects acme/<name> to a server that holds every request
// until release is called; hit is closed at the first request.
func (x *h) pauseFetch(name string) (hit <-chan struct{}, release func()) {
	x.t.Helper()
	h, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(h) })
		select {
		case <-done:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	}))
	var rel sync.Once
	release = func() { rel.Do(func() { close(done) }) }
	x.t.Cleanup(srv.Close)
	x.t.Cleanup(release)
	git(x.t, x.dir, "config", "--file", x.gitcfg, "--remove-section", "url."+filepath.Join(x.dir, "origins", name+".git"))
	git(x.t, x.dir, "config", "--file", x.gitcfg, "url."+srv.URL+"/"+name+".git.insteadOf", "https://github.com/acme/"+name)
	return h, release
}

// A re-registration while a cycle runs (paused in its anchor fetch, after
// it loaded the registration and before it saves it back) is never undone
// by that cycle (A-M4): either `project add` waits its turn and says so, or
// what it registered is what stays.
func TestReRegistrationDuringCycleIsNotUndone(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	reg := x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	old := project.FetchBudget
	project.FetchBudget = time.Second
	defer func() { project.FetchBudget = old }()
	hit, release := x.pauseFetch("admin")
	done := make(chan error, 1)
	go func() {
		_, err := x.runner().Cycle(context.Background(), "shop", true)
		done <- err
	}()
	select {
	case <-hit:
	case <-time.After(30 * time.Second):
		t.Fatal("the cycle never fetched the anchor")
	}
	moved := *reg
	moved.Store = filepath.Join(x.dir, "data", "moved.db")
	rerr := project.Register(x.state, &moved)
	release()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the cycle didn't finish")
	}
	got, err := project.Load(x.state, "shop")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case rerr == nil && got.Store != moved.Store:
		t.Errorf("project add reported success, then the running cycle put the old registration back (store %s, want %s)", got.Store, moved.Store)
	case rerr != nil && got.Store != reg.Store:
		t.Errorf("project add refused (%v), yet the registration changed to store %s", rerr, got.Store)
	}
	if rerr == nil {
		t.Errorf("project add during a running cycle succeeded without the lock")
	}
}
