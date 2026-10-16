package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepNudgesOncePerSessionInMappedRepos(t *testing.T) {
	data := t.TempDir()
	mapped := func(_ context.Context, cwd string) bool { return cwd == "/work/app" }
	run := func(payload string) string {
		var out bytes.Buffer
		Grep(context.Background(), strings.NewReader(payload), &out, data, mapped)
		return out.String()
	}
	first := run(`{"session_id":"s1","cwd":"/work/app","hook_event_name":"PreToolUse"}`)
	if !strings.Contains(first, `"additionalContext":"cartographer has a map`) || strings.Contains(first, "permissionDecision") {
		t.Fatalf("first = %q", first)
	}
	if again := run(`{"session_id":"s1","cwd":"/work/app"}`); again != "" {
		t.Errorf("second call in the session should be silent: %q", again)
	}
	if other := run(`{"session_id":"s2","cwd":"/elsewhere"}`); other != "" {
		t.Errorf("unmapped repo should be silent: %q", other)
	}
	if bad := run(`not json`); bad != "" {
		t.Errorf("bad input should be silent: %q", bad)
	}
	if traversal := run(`{"session_id":"../../x","cwd":"/work/app"}`); !strings.Contains(traversal, "additionalContext") {
		t.Errorf("unsafe session ids skip the marker but still nudge: %q", traversal)
	}
}

func TestGrepChecksUnmappedSessionsOnce(t *testing.T) {
	data := t.TempDir()
	calls := 0
	mapped := func(context.Context, string) bool { calls++; return false }
	for range 3 {
		var out bytes.Buffer
		Grep(context.Background(), strings.NewReader(`{"session_id":"u1","cwd":"/elsewhere"}`), &out, data, mapped)
		if out.Len() != 0 {
			t.Fatalf("unmapped repo should be silent: %q", out.String())
		}
	}
	if calls != 1 {
		t.Errorf("mapped ran %d times; an unmapped session should be scanned once", calls)
	}
}

func TestGrepFailsOpenWhenMarkerUnwritable(t *testing.T) {
	data := filepath.Join(t.TempDir(), "file")
	os.WriteFile(data, nil, 0o644) // a file where the data directory should be
	var out bytes.Buffer
	Grep(context.Background(), strings.NewReader(`{"session_id":"s1","cwd":"/work/app"}`), &out, data, func(context.Context, string) bool { return true })
	if !strings.Contains(out.String(), "additionalContext") {
		t.Errorf("unwritable marker must still nudge: %q", out.String())
	}
}
