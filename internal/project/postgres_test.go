package project_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// realPostgres returns a Mise using the real mise once it provides the
// release's PostgreSQL (installing it if need be), and the PostgreSQL
// programs' directory; it skips the test when it can't.
func realPostgres(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("managed PostgreSQL runs on unix")
	}
	bin, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("no mise on PATH")
	}
	m := &project.Mise{Bin: bin, Dir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	paths, err := m.BinPaths(ctx, project.Postgres, "postgres", "initdb", "psql")
	if err != nil {
		t.Skipf("mise can't provide %s: %v", project.Postgres.Spec(), err)
	}
	return bin, paths[0]
}

// leftovers lists what remains of a schema run under dirs, checked at
// once (stop waits for the cluster to exit): processes whose working
// directory is inside them (the postmaster and its backends work in the
// data directory; a prepare in the archive) and cluster or socket
// directories.
func leftovers(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	if runtime.GOOS == "linux" {
		procs, _ := filepath.Glob("/proc/[0-9]*/cwd")
		for _, p := range procs {
			cwd, err := os.Readlink(p)
			if err != nil {
				continue
			}
			for _, d := range dirs {
				if strings.HasPrefix(cwd, d+string(filepath.Separator)) || cwd == d {
					cmd, _ := os.ReadFile(filepath.Join(filepath.Dir(p), "cmdline"))
					out = append(out, "process "+filepath.Base(filepath.Dir(p))+" "+strings.ReplaceAll(string(cmd), "\x00", " ")+" in "+cwd)
				}
			}
		}
	}
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.IsDir() && (e.Name() == "pgdata" || strings.HasPrefix(e.Name(), "cartographer-schema-db-") || strings.HasPrefix(e.Name(), "cartographer-pg-")) {
				out = append(out, "directory "+p)
				return filepath.SkipDir
			}
			return nil
		})
	}
	return out
}

func listening(port string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err == nil {
		c.Close()
	}
	return err == nil
}

// The release's PostgreSQL ships the extensions breakdown-admin's
// migrations create, and its cluster is gone once stopped.
func TestManagedPostgresExtensions(t *testing.T) {
	miseBin, pgBin := realPostgres(t)
	t.Setenv("TMPDIR", t.TempDir())
	scratch := filepath.Join(t.TempDir(), "db")
	os.MkdirAll(scratch, 0o755)
	pg := &project.ManagedPostgres{Mise: &project.Mise{Bin: miseBin, Dir: t.TempDir()}}
	vars, stop, err := pg.Start(context.Background(), scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if vars["pg_host"] != "127.0.0.1" || vars["pg_port"] == "" || vars["pg_port"] == "5432" {
		t.Fatalf("vars %v", vars)
	}
	out, err := exec.Command(filepath.Join(pgBin, "psql"), "-X", "-At", "-h", vars["pg_host"], "-p", vars["pg_port"], "-U", "postgres", "-d", "postgres",
		"-c", "CREATE EXTENSION citext", "-c", "CREATE EXTENSION pg_trgm",
		"-c", "SELECT string_agg(extname, ',' ORDER BY extname) FROM pg_extension",
		"-c", "SELECT 'Ab'::citext = 'aB'::citext, similarity('cartographer', 'cartograph') > 0.5",
		"-c", "SHOW server_version", "-c", "SELECT datcollate || ' ' || pg_encoding_to_char(encoding) FROM pg_database WHERE datname = 'postgres'", "-c", "SHOW listen_addresses").CombinedOutput()
	if err != nil {
		t.Fatalf("psql: %v\n%s", err, out)
	}
	want := "CREATE EXTENSION\nCREATE EXTENSION\ncitext,pg_trgm,plpgsql\nt|t\n" + project.Postgres.Version + "\nC UTF8\n127.0.0.1\n"
	if string(out) != want {
		t.Errorf("psql printed\n%s\nwant\n%s", out, want)
	}
	stop()
	if listening(vars["pg_port"]) {
		t.Errorf("port %s still accepts connections after stop", vars["pg_port"])
	}
	if l := leftovers(t, scratch, os.Getenv("TMPDIR")); len(l) > 0 {
		t.Errorf("left after stop:\n%s", strings.Join(l, "\n"))
	}
}

// pgWorld is the harness world with admin declaring a managed schema step:
// a fake mise passing the PostgreSQL pin to the real one, a prepare that
// creates the distinctive table in the throwaway cluster through
// TEST_DB_PORT={pg_port} (logging each run and port), and a capture plugin
// that lists the cluster's tables through --port {pg_port}.
func pgWorld(t *testing.T, prepareTail string) (*h, string) {
	miseBin, _ := realPostgres(t)
	x := newH(t)
	t.Setenv("TMPDIR", filepath.Join(x.dir, "tmp"))
	os.MkdirAll(os.Getenv("TMPDIR"), 0o755)
	fake, _ := os.ReadFile(filepath.Join(x.dir, "mise"))
	pass := "#!/bin/sh\ncase \"$*\" in *postgres@*) [ \"$1\" != env ] && exec '" + miseBin + "' \"$@\" ;; esac\n"
	os.WriteFile(filepath.Join(x.dir, "mise"), []byte(pass+strings.TrimPrefix(string(fake), "#!/bin/sh\n")), 0o755)
	cap := filepath.Join(x.plugins, "fake-capture")
	os.MkdirAll(cap, 0o755)
	os.WriteFile(filepath.Join(cap, "capture.sh"), []byte(`#!/bin/sh
echo capture >> '`+filepath.Join(x.dir, "schema.log")+`'
while [ $# -gt 0 ]; do case $1 in --host) host=$2; shift 2 ;; --port) port=$2; shift 2 ;; *) shift ;; esac; done
tables=$(psql -X -At -h "$host" -p "$port" -U postgres -d cartographer_t -c "SELECT string_agg('{\"id\":\"public.' || table_name || '\",\"schema\":\"public\",\"name\":\"' || table_name || '\"}', ',' ORDER BY table_name) FROM information_schema.tables WHERE table_schema = 'public'") || exit 1
printf '{"facts_version":2,"producer":{"name":"fake-capture","version":"1"},"tables":[%s]}\n' "$tables"
`), 0o755)
	log, ports := filepath.Join(x.dir, "schema.log"), filepath.Join(x.dir, "ports.log")
	prepare := `echo prepare >> ` + log + `; echo $TEST_DB_PORT >> ` + ports + `; ` +
		`psql -X -q -h 127.0.0.1 -p $TEST_DB_PORT -U postgres -c 'CREATE DATABASE cartographer_t' && ` +
		`psql -X -q -h 127.0.0.1 -p $TEST_DB_PORT -U postgres -d cartographer_t -c 'CREATE TABLE only_in_throwaway (id int PRIMARY KEY)'` + prepareTail
	_, sales, _ := x.world()
	x.push("admin", map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`,
			"[schema]", `postgres = "managed"`, `prepare = "`+strings.ReplaceAll(prepare, `"`, `\"`)+`"`, `cache_key = ["priv/migrations"]`,
			`env = { MIX_ENV = "test", TEST_DB_PORT = "{pg_port}" }`,
			`capture = ["sh", "{plugins}/fake-capture/capture.sh", "--host", "{pg_host}", "--port", "{pg_port}"]`),
		"priv/migrations/001.exs": "create table(:only_in_throwaway)\n",
	})
	x.register(sales)
	return x, sales
}

func readLines(p string) []string {
	b, _ := os.ReadFile(p)
	return strings.Fields(string(b))
}

// The managed schema step captures what prepare made in the throwaway
// cluster and leaves nothing behind; a second build with the schema
// unchanged hits the cache (prepare and capture ran once); and a build
// elsewhere gets another port but the same cache key.
func TestManagedPostgresSchema(t *testing.T) {
	x, _ := pgWorld(t, "")
	r := x.runner()
	res, err := r.Cycle(context.Background(), "shop", true)
	if err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	rows, err := x.s.DB.Query(`SELECT id FROM db_tables WHERE snapshot_id = ? ORDER BY id`, res.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		tables = append(tables, id)
	}
	rows.Close()
	if strings.Join(tables, ",") != "public.only_in_throwaway" {
		t.Errorf("captured tables %v, want only public.only_in_throwaway", tables)
	}
	ports := readLines(filepath.Join(x.dir, "ports.log"))
	if len(ports) != 1 || ports[0] == "5432" || ports[0] == "{pg_port}" {
		t.Fatalf("prepare saw TEST_DB_PORT %v", ports)
	}
	if listening(ports[0]) {
		t.Errorf("port %s still accepts connections", ports[0])
	}
	if l := leftovers(t, x.state, os.Getenv("TMPDIR")); len(l) > 0 {
		t.Errorf("left after success:\n%s", strings.Join(l, "\n"))
	}
	keys, _ := filepath.Glob(filepath.Join(x.cache, "schema", "*.json"))
	if len(keys) != 1 {
		t.Fatalf("schema cache %v", keys)
	}

	x.push("admin", map[string]string{"lib/other.ex": "x\n"}) // outside cache_key
	if res, err := r.Cycle(context.Background(), "shop", true); err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if got := strings.Join(readLines(filepath.Join(x.dir, "schema.log")), " "); got != "prepare capture" {
		t.Errorf("schema step ran %q over two builds, want prepare and capture once", got)
	}

	// Elsewhere (its own store, state and cache): a cluster on another
	// port, the same key.
	other := t.TempDir()
	s2, err := store.Open(filepath.Join(other, "carto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	reg, _ := project.Load(x.state, "shop")
	project.Save(filepath.Join(other, "state"), reg)
	r2 := x.runner()
	r2.Store, r2.StateDir, r2.CacheDir = s2, filepath.Join(other, "state"), filepath.Join(other, "cache")
	r2.Mise.Dir = r2.StateDir
	if res, err := r2.Cycle(context.Background(), "shop", true); err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	ports = readLines(filepath.Join(x.dir, "ports.log"))
	if len(ports) != 2 || ports[0] == ports[1] {
		t.Errorf("ports %v: want two runs on different ports", ports)
	}
	keys2, _ := filepath.Glob(filepath.Join(r2.CacheDir, "schema", "*.json"))
	if len(keys2) != 1 || filepath.Base(keys2[0]) != filepath.Base(keys[0]) {
		t.Errorf("cache keys %v and %v differ", keys, keys2)
	}
	if l := leftovers(t, r2.StateDir, os.Getenv("TMPDIR")); len(l) > 0 {
		t.Errorf("left after the second cluster:\n%s", strings.Join(l, "\n"))
	}
}

// A prepare that fails leaves no cluster behind.
func TestManagedPostgresPrepareFails(t *testing.T) {
	x, _ := pgWorld(t, "; exit 1")
	_, err := x.runner().Cycle(context.Background(), "shop", true)
	if err == nil || !strings.Contains(err.Error(), "schema prepare") {
		t.Fatalf("want a schema prepare failure, got %v", err)
	}
	ports := readLines(filepath.Join(x.dir, "ports.log"))
	if len(ports) != 1 {
		t.Fatalf("prepare ran with ports %v", ports)
	}
	if listening(ports[0]) {
		t.Errorf("port %s still accepts connections", ports[0])
	}
	if l := leftovers(t, x.state, os.Getenv("TMPDIR")); len(l) > 0 {
		t.Errorf("left after failure:\n%s", strings.Join(l, "\n"))
	}
}

// Cancelling mid-prepare stops the cluster and removes it.
func TestManagedPostgresCancelled(t *testing.T) {
	x, _ := pgWorld(t, "")
	started := filepath.Join(x.dir, "started")
	// Prepare creates the table, says so, then hangs.
	b, _ := os.ReadFile(filepath.Join(x.upstream["admin"], ".cartographer", "repo.toml"))
	toml := strings.Replace(string(b), `(id int PRIMARY KEY)'"`, `(id int PRIMARY KEY)' && touch `+started+` && sleep 60"`, 1)
	if toml == string(b) {
		t.Fatal("prepare not rewritten")
	}
	x.push("admin", map[string]string{".cartographer/repo.toml": toml})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := x.runner().Cycle(ctx, "shop", true)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("cycle ended before prepare hung: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("prepare never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	ports := readLines(filepath.Join(x.dir, "ports.log"))
	if len(ports) != 1 || !listening(ports[0]) {
		t.Fatalf("no cluster mid-prepare (ports %v)", ports)
	}
	at := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled cycle succeeded")
		}
	case <-time.After(time.Minute):
		t.Fatal("cancelled cycle didn't return")
	}
	t.Logf("cancel returned after %s", time.Since(at))
	if d := time.Since(at); d > 20*time.Second {
		t.Errorf("cancel took %s", d)
	}
	if listening(ports[0]) {
		t.Errorf("port %s still accepts connections", ports[0])
	}
	if l := leftovers(t, x.state, os.Getenv("TMPDIR")); len(l) > 0 {
		t.Errorf("left after cancel:\n%s", strings.Join(l, "\n"))
	}
}

// fakeDB is a SchemaDB that starts nothing and names a fixed port.
type fakeDB struct{}

func (fakeDB) Start(context.Context, string) (map[string]string, func(), error) {
	return map[string]string{"pg_host": "127.0.0.1", "pg_port": "55999"}, func() {}, nil
}

// The repo's [schema] env beats the caller's environment, and libpq's PG*
// variables don't reach a managed step: an MCP or shell environment that
// already sets TEST_DB_PORT, MIX_ENV or PGHOST can't point the step at
// another database.
func TestSchemaEnvBeatsCallerEnvironment(t *testing.T) {
	x := newH(t)
	x.fakeTool(project.Postgres.Spec(), "postgres", "#!/bin/sh\nexit 0\n")
	t.Setenv("TEST_DB_PORT", "5432")
	t.Setenv("MIX_ENV", "prod")
	t.Setenv("PGHOST", "docker.example")
	seen := filepath.Join(x.dir, "seen")
	_, sales, _ := x.world()
	x.push("admin", map[string]string{
		".cartographer/repo.toml": settings("shop", "", `members = [{repo = "acme/admin"}, {repo = "acme/sales"}, {repo = "acme/infra"}]`, `extract = ["elixir"]`,
			"[schema]", `postgres = "managed"`, `prepare = "echo $TEST_DB_PORT $MIX_ENV ${PGHOST:-none} > `+seen+`"`,
			`env = { MIX_ENV = "test", TEST_DB_PORT = "{pg_port}" }`,
			`capture = ["sh", "-c", "cat {root}/schema.json"]`),
		"schema.json": `{"facts_version":2,"producer":{"name":"schema","version":"1"},"tables":[]}`,
	})
	x.register(sales)
	r := x.runner()
	r.SchemaDB = fakeDB{}
	if _, err := r.Cycle(context.Background(), "shop", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(seen)
	if got := strings.TrimSpace(string(b)); got != "55999 test none" {
		t.Errorf("prepare saw TEST_DB_PORT, MIX_ENV, PGHOST = %q, want \"55999 test none\"", got)
	}
}
