package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/store"
)

// v2As copies the format-2 fixture under another workspace, id and source.
func v2As(t *testing.T, workspace, id, source string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(fixtureV2, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureV2, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "manifest.json" {
			b = []byte(strings.NewReplacer(`"workspace": "v2"`, `"workspace": "`+workspace+`"`,
				"snapshot-20261120T000000Z", id, `"source": "main"`, `"source": "`+source+`"`).Replace(string(b)))
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

const (
	alphaOld  = "snapshot-20261120T000000Z"
	alphaMain = "snapshot-20261121T000000Z"
	alphaPub  = "snapshot-20261122T000000Z"
	betaMain  = "snapshot-20261123T000000Z"
)

// projectStore holds two alpha main builds, a newer published alpha
// snapshot, and the newest of all, beta's main build.
func projectStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, v := range [][3]string{{"alpha", alphaOld, "main"}, {"alpha", alphaMain, "main"}, {"alpha", alphaPub, "published"}, {"beta", betaMain, "main"}} {
		if _, err := s.Import(context.Background(), v2As(t, v[0], v[1], v[2])); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func serveOpts(t *testing.T, o Options) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(o))
	t.Cleanup(srv.Close)
	return srv
}

func TestProjectParamOpensNewestMainBuild(t *testing.T) {
	s := projectStore(t)
	srv := serveOpts(t, Options{Store: s})
	st := getJSON[obj](t, srv, "/api/status?project=alpha")
	if st["snapshot"] != alphaMain || st["default_snapshot"] != alphaMain {
		t.Errorf("project=alpha: snapshot %v, default %v; want %s (newest main, not the published one)", st["snapshot"], st["default_snapshot"], alphaMain)
	}
	if st := getJSON[obj](t, srv, "/api/status"); st["snapshot"] != betaMain {
		t.Errorf("no project: snapshot %v, want the newest overall", st["snapshot"])
	}
	// An explicit snapshot wins; the default stays the project's.
	st = getJSON[obj](t, srv, "/api/status?project=alpha&snapshot="+alphaOld)
	if st["snapshot"] != alphaOld || st["default_snapshot"] != alphaMain {
		t.Errorf("project with snapshot: %v / %v", st["snapshot"], st["default_snapshot"])
	}
	// Every snapshot-scoped endpoint follows it.
	if ov := getJSON[obj](t, srv, "/api/overview?project=alpha"); asObj(ov["manifest"])["snapshot"] != alphaMain {
		t.Errorf("overview?project=alpha: %v", asObj(ov["manifest"])["snapshot"])
	}
	var ids []string
	for _, sn := range getJSON[[]obj](t, srv, "/api/snapshots?project=alpha") {
		ids = append(ids, sn["id"].(string))
		if sn["source"] != "main" {
			t.Errorf("snapshot %v source %v", sn["id"], sn["source"])
		}
	}
	if strings.Join(ids, ",") != alphaMain+","+alphaOld {
		t.Errorf("snapshots?project=alpha = %v, want alpha's main builds newest first", ids)
	}
	if all := getJSON[[]obj](t, srv, "/api/snapshots"); len(all) != 4 {
		t.Errorf("snapshots without project: %d, want 4", len(all))
	}
	// With an explicit snapshot, a project that resolves to nothing doesn't
	// fail status (R1-m3): snapshot wins, as on every endpoint.
	if st := getJSON[obj](t, srv, "/api/status?project=nope&snapshot="+alphaOld); st["snapshot"] != alphaOld {
		t.Errorf("status with snapshot and an unknown project: %v", st["snapshot"])
	}
	body, _ := get(t, srv, "/api/status?project=nope", 404)
	if !strings.Contains(body, "no map of project nope") {
		t.Errorf("unknown project: %s", body)
	}
}

type notFoundErr struct{}

func (notFoundErr) Error() string  { return "no map of project alpha yet: its first build has started" }
func (notFoundErr) NotFound() bool { return true }

func TestProjectSnapshotHook(t *testing.T) {
	s := projectStore(t)
	var asked []string
	srv := serveOpts(t, Options{Store: s, ProjectSnapshot: func(_ context.Context, name string) (string, error) {
		asked = append(asked, name)
		switch name {
		case "alpha":
			return alphaOld, nil
		case "building":
			return "", notFoundErr{}
		}
		return "", errors.New("registry unreadable")
	}})
	if st := getJSON[obj](t, srv, "/api/status?project=alpha"); st["snapshot"] != alphaOld {
		t.Errorf("hook's answer not used: %v", st["snapshot"])
	}
	if body, _ := get(t, srv, "/api/catalog?project=building", 404); !strings.Contains(body, "first build has started") {
		t.Errorf("first-build text lost: %s", body)
	}
	get(t, srv, "/api/catalog?project=other", 500)
	if len(asked) == 0 {
		t.Error("hook never called")
	}
}

func TestProjectsEndpoint(t *testing.T) {
	s := projectStore(t)
	empty := serveOpts(t, Options{Store: s})
	if v := getJSON[obj](t, empty, "/api/projects"); v["projects"] == nil || len(list(v["projects"])) != 0 || v["problems"] == nil {
		t.Errorf("no hook: %v, want empty lists", v)
	}
	srv := serveOpts(t, Options{Store: s, Projects: func(context.Context) (any, error) {
		return obj{"projects": []obj{{"name": "alpha"}}, "problems": []obj{}}, nil
	}})
	if v := getJSON[obj](t, srv, "/api/projects"); asObj(list(v["projects"])[0])["name"] != "alpha" {
		t.Errorf("projects: %v", v)
	}
	bad := serveOpts(t, Options{Store: s, Projects: func(context.Context) (any, error) { return nil, errors.New("permission denied") }})
	if body, _ := get(t, bad, "/api/projects", 500); !strings.Contains(body, "permission denied") {
		t.Errorf("registry error: %s", body)
	}
}
