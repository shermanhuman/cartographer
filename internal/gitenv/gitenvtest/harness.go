// Package gitenvtest is the hermetic harness for prompt tests: a loopback
// git endpoint that demands credentials, and a recording askpass program.
// Tests use it with an environment built from nothing, so neither the
// machine's git config nor its credential helpers can answer for them.
package gitenvtest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Harness is one test's endpoint and recorder.
type Harness struct {
	URL      string // a git https-style URL that answers 401
	Recorder string // an askpass program that records being run
	hits     *atomic.Int32
	log      string
}

// New starts the endpoint and writes the recorder, and makes the test
// process's environment hermetic: a temp HOME and global config, no system
// config, no config injection, and no tokens.
func New(t *testing.T) *Harness {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	log := filepath.Join(dir, "askpass.log")
	rec := filepath.Join(dir, "askpass")
	if err := os.WriteFile(rec, []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\necho secret\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	os.WriteFile(global, nil, 0o644)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return &Harness{URL: srv.URL + "/repo.git", Recorder: rec, hits: &hits, log: log}
}

// Hits is how many requests reached the endpoint.
func (h *Harness) Hits() int { return int(h.hits.Load()) }

// Prompted reports whether the recorder ran, and with what prompts.
func (h *Harness) Prompted() (bool, string) {
	b, err := os.ReadFile(h.log)
	return err == nil, strings.TrimSpace(string(b))
}

// Check fails the test unless the endpoint was reached (so the credential
// request really happened) and the recorder never ran.
func (h *Harness) Check(t *testing.T, what string) {
	t.Helper()
	if h.Hits() == 0 {
		t.Fatalf("%s: the endpoint was never reached, so nothing asked for credentials", what)
	}
	if ran, prompts := h.Prompted(); ran {
		t.Fatalf("%s: an askpass prompt ran: %s", what, prompts)
	}
}
