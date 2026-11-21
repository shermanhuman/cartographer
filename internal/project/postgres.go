package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// ManagedPostgres is the throwaway PostgreSQL cluster of a `postgres =
// "managed"` schema step (a workspace.SchemaDB): Start initialises a
// cluster in the build's scratch directory (user postgres, trust auth,
// UTF8, C locale), runs `postgres` as a tracked child listening on
// 127.0.0.1 only at a free port with its Unix socket beside the data, and
// waits until it accepts connections; stop ends it (SIGTERM, then SIGKILL
// to its process group after StopTimeout) and removes what Start made.
type ManagedPostgres struct {
	Mise *Mise
	// Tool is the PostgreSQL to run (default Postgres).
	Tool Tool
	// ReadyTimeout bounds the wait for connections (default 60 s);
	// StopTimeout the wait after SIGTERM (default 10 s).
	ReadyTimeout, StopTimeout time.Duration
	Log                       io.Writer
}

// pgBins are the programs Start runs and the capture needs.
var pgBins = []string{"postgres", "initdb", "pg_isready", "psql"}

func (p *ManagedPostgres) tool() Tool {
	if p.Tool.Name == "" {
		return Postgres
	}
	return p.Tool
}

// Key is the cluster's identity in the schema cache key.
func (p *ManagedPostgres) Key() string { return "postgresql " + p.tool().Version }

// Env is the environment that puts the cluster's programs (psql) first on
// PATH, for a schema step that gets no tools from mise otherwise (a
// cartographer.toml build).
func (p *ManagedPostgres) Env(ctx context.Context) ([]string, error) {
	if p.Mise == nil {
		return nil, errors.New("managed postgres: no tool runner")
	}
	bins, err := p.Mise.BinPaths(ctx, p.tool(), pgBins...)
	if err != nil {
		return nil, err
	}
	return []string{"PATH=" + strings.Join(append(bins, filepath.SplitList(os.Getenv("PATH"))...), string(os.PathListSeparator))}, nil
}

// sockMax is the longest Unix socket path (sun_path less its NUL on
// Linux; macOS allows 103).
const sockMax = 103

// Start implements workspace.SchemaDB: vars are pg_host and pg_port.
func (p *ManagedPostgres) Start(ctx context.Context, scratch string) (map[string]string, func(), error) {
	if p.Mise == nil {
		return nil, nil, errors.New("managed postgres: no tool runner")
	}
	t := p.tool()
	bins, err := p.Mise.BinPaths(ctx, t, pgBins...)
	if err != nil {
		return nil, nil, err
	}
	env, err := p.Mise.Env(ctx, []Tool{t})
	if err != nil {
		return nil, nil, err
	}
	env = withoutPG(env)
	own := []string{"PATH=" + strings.Join(bins, string(os.PathListSeparator))}
	lookup := func(name string) string {
		q, _ := lookIn(own, name) // BinPaths checked each resolves
		return q
	}

	var cleanup []func()
	clean := func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}
	data := filepath.Join(scratch, "pgdata")
	cleanup = append(cleanup, func() { os.RemoveAll(data); os.Remove(filepath.Join(scratch, "postgres.log")) })
	// The socket goes beside the data unless the path is too long for a
	// socket; then into a private directory of its own.
	sock := scratch
	if len(filepath.Join(sock, ".s.PGSQL.65535")) > sockMax {
		if sock, err = os.MkdirTemp("", "cartographer-pg-"); err != nil {
			return nil, nil, err
		}
		dir := sock
		cleanup = append(cleanup, func() { os.RemoveAll(dir) })
	}
	if _, err := workspace.RunCmd(ctx, scratch, env, []string{lookup("initdb"), "-U", "postgres", "-E", "UTF8", "--locale=C", "--auth=trust", "--no-sync", "--no-instructions", "-D", data}, nil); err != nil {
		clean()
		return nil, nil, fmt.Errorf("initdb: %w", err)
	}

	ready := p.ReadyTimeout
	if ready == 0 {
		ready = 60 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		port, err := freePort()
		if err != nil {
			clean()
			return nil, nil, err
		}
		srv, err := p.serve(ctx, scratch, env, lookup("postgres"), data, sock, port)
		if err != nil {
			clean()
			return nil, nil, err
		}
		err = srv.waitReady(ctx, env, lookup("pg_isready"), port, ready)
		if err == nil {
			if p.Log != nil {
				fmt.Fprintf(p.Log, "schema: PostgreSQL %s on 127.0.0.1:%d\n", t.Version, port)
			}
			var once sync.Once
			stop := func() {
				once.Do(func() {
					srv.stop(p.StopTimeout)
					clean()
				})
			}
			return map[string]string{"pg_host": "127.0.0.1", "pg_port": strconv.Itoa(port)}, stop, nil
		}
		srv.stop(p.StopTimeout)
		lastErr = err
		// Another process took the port between freePort and the bind:
		// try another.
		if !strings.Contains(srv.log(), "could not bind") && !strings.Contains(srv.log(), "already in use") {
			break
		}
	}
	clean()
	return nil, nil, lastErr
}

// withoutPG drops libpq's PG* variables, so nothing run against the
// cluster is pointed at another server by the caller's environment.
func withoutPG(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PG") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// pgServer is one running postmaster.
type pgServer struct {
	cmd     *exec.Cmd
	done    chan struct{}
	logf    string
	untrack func()
}

func (p *ManagedPostgres) serve(ctx context.Context, scratch string, env []string, bin, data, sock string, port int) (*pgServer, error) {
	logPath := filepath.Join(scratch, "postgres.log")
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	// Durability is pointless for a cluster deleted after one run.
	cmd := exec.Command(bin, "-D", data, "-h", "127.0.0.1", "-p", strconv.Itoa(port), "-k", sock,
		"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "logging_collector=off")
	cmd.Dir = scratch
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = lf, lf
	// Its own process group, so a kill reaches the backends too.
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}
	s := &pgServer{cmd: cmd, done: make(chan struct{}), logf: logPath, untrack: workspace.TrackGroup(cmd.Process.Pid)}
	go func() {
		cmd.Wait()
		lf.Close()
		close(s.done)
	}()
	return s, nil
}

func (s *pgServer) log() string {
	b, _ := os.ReadFile(s.logf)
	return string(b)
}

// waitReady polls pg_isready until the server accepts connections.
func (s *pgServer) waitReady(ctx context.Context, env []string, isready string, port int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return fmt.Errorf("postgres exited while starting: %s", workspace.Tail(strings.TrimSpace(s.log())))
		case <-tick.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(cctx, isready, "-q", "-h", "127.0.0.1", "-p", strconv.Itoa(port), "-U", "postgres", "-d", "postgres", "-t", "2")
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres didn't accept connections within %s: %s", limit, workspace.Tail(strings.TrimSpace(s.log())))
		}
	}
}

// stop sends SIGTERM and waits, then SIGKILLs the process group.
func (s *pgServer) stop(timeout time.Duration) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	defer s.untrack()
	select {
	case <-s.done:
		return
	default:
	}
	s.cmd.Process.Signal(syscall.SIGTERM) // smart shutdown
	select {
	case <-s.done:
		return
	case <-time.After(timeout):
	}
	killGroup(s.cmd.Process)
	<-s.done
}
