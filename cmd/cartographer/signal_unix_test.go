//go:build unix

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/rebuild"
)

// SIGTERM to `cartographer mcp` mid-rebuild cancels the rebuild and waits
// for its cleanup: lock and scratch are gone, the plugin's grandchild is
// dead, and nothing is recorded as a failure.
func TestMCPSignalCleansUpARunningRebuild(t *testing.T) {
	_, repo, ws := localWorkspace(t)
	db := filepath.Join(t.TempDir(), "carto.db")
	cli(t, db, "build", "--local", "--import", "--workspace", ws)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead")
	gpid := filepath.Join(t.TempDir(), "grandchild")
	toml, _ := os.ReadFile(filepath.Join(ws, "cartographer.toml"))
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), bytes.Replace(toml, []byte(`["cat", "`), []byte(`["sh", "-c", "sleep 600 & echo $! > '`+gpid+`'; wait", "`), 1), 0o644)
	register(t, ws)

	self, _ := os.Executable()
	cmd := exec.Command(self, "mcp", "--db", db, "--no-pull")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "CARTOGRAPHER_TEST_CLI=1", "CARTOGRAPHER_NO_REBUILD=")
	stdin, _ := cmd.StdinPipe()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"map_status","arguments":{}}}
`)
	var pid int
	for deadline := time.Now().Add(20 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(gpid)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatalf("the rebuild's plugin never started; stderr:\n%s", stderr.String())
	}
	d := rebuild.Dir{Path: filepath.Dir(db)}
	if _, running := d.Running("mini"); !running {
		t.Fatal("no lock while the plugin runs")
	}
	cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("mcp did not exit after SIGTERM; stderr:\n%s", stderr.String())
	}
	if _, running := d.Running("mini"); running {
		t.Error("lock left behind")
	}
	if m := scratch(d); len(m) != 0 {
		t.Errorf("scratch left behind: %v", m)
	}
	if !d.Due("mini") || d.Status("mini") != "" {
		t.Errorf("the cancelled rebuild was recorded: %q", d.Status("mini"))
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("the plugin's grandchild %d survived", pid)
}

// A second Ctrl-C exits at once and takes the plugins with it: their
// process groups (which no longer get the terminal's signals) are killed,
// even a plugin that ignores SIGTERM.
func TestSecondInterruptKillsThePlugins(t *testing.T) {
	_, _, ws := localWorkspace(t)
	gpid := filepath.Join(t.TempDir(), "grandchild")
	toml, _ := os.ReadFile(filepath.Join(ws, "cartographer.toml"))
	os.WriteFile(filepath.Join(ws, "cartographer.toml"), bytes.Replace(toml, []byte(`["cat", "`), []byte(`["sh", "-c", "trap '' TERM; sleep 600 & echo $! > '`+gpid+`'; wait", "`), 1), 0o644)
	db := filepath.Join(t.TempDir(), "carto.db")
	self, _ := os.Executable()
	cmd := exec.Command(self, "build", "--local", "--import", "--workspace", ws, "--db", db)
	cmd.Env = append(os.Environ(), "CARTOGRAPHER_TEST_CLI=1")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var pid int
	for deadline := time.Now().Add(20 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(gpid)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatalf("the plugin never started; stderr:\n%s", stderr.String())
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cmd.Process.Signal(os.Interrupt)
	time.Sleep(500 * time.Millisecond) // the plugin ignores the group's SIGTERM
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the TERM-ignoring plugin died on the first interrupt")
	}
	cmd.Process.Signal(os.Interrupt)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the second interrupt did not exit")
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	t.Errorf("plugin %d survived the second interrupt", pid)
}
