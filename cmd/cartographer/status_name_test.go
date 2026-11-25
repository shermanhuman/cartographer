package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/testfixture"
)

// status <name> shows that project's newest main map from anywhere, even
// with several projects in one store and a newer published snapshot.
func TestStatusTakesAProjectName(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "carto.db")
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range [][3]string{{"alpha", "snapshot-20261121T000000Z", "main"}, {"alpha", "snapshot-20261123T000000Z", "published"}, {"sengastudio", "snapshot-20261122T000000Z", "main"}} {
		if _, err := s.Import(ctx, testfixture.V2As(t, v[0], v[1], v[2])); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	os.Chdir(t.TempDir()) // outside any checkout
	status := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, append([]string{"status", "--db", db, "--no-pull", "--no-rebuild"}, args...), &out)
		return out.String(), err
	}
	if out, err := status("sengastudio"); err != nil || !strings.Contains(out, "snapshot-20261122T000000Z") {
		t.Errorf("status sengastudio: %v\n%s", err, out)
	}
	if out, err := status("alpha"); err != nil || !strings.Contains(out, "snapshot-20261121T000000Z") || strings.Contains(out, "snapshot-20261123T000000Z") {
		t.Errorf("status alpha: want its main map, not the newer published one: %v\n%s", err, out)
	}
	if _, err := status("nope"); err == nil || !strings.Contains(err.Error(), "registered projects") {
		t.Errorf("status nope: %v", err)
	}
	if _, err := status("alpha", "sengastudio"); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("two names: %v", err)
	}
	if out, err := status("alpha", "--snapshot", "snapshot-20261122T000000Z"); err != nil || !strings.Contains(out, "snapshot-20261122T000000Z") {
		t.Errorf("--snapshot should win: %v\n%s", err, out)
	}
}
