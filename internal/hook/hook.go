// Package hook implements the agent-host hook commands. Hooks fail open:
// any error means "say nothing", never a blocked tool call.
package hook

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Input is the subset of the host's hook payload we read.
type Input struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
}

// Nudge is the one line added before the first Grep of a session in a mapped repo.
const Nudge = "cartographer has a map of this repository: the map_find / map_callers / map_impact tools answer where-defined, who-calls and what-breaks questions with fewer tokens than grep."

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Grep handles a PreToolUse(Grep) hook. mapped reports whether a directory is
// inside a mapped repository; it runs at most once per session. Grep writes
// nothing unless it should nudge.
func Grep(ctx context.Context, in io.Reader, out io.Writer, dataDir string, mapped func(ctx context.Context, cwd string) bool) {
	var h Input
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&h); err != nil || h.Cwd == "" {
		return
	}
	dir := filepath.Join(dataDir, "hook-sessions")
	var marker string
	if safeID.MatchString(h.SessionID) {
		marker = filepath.Join(dir, h.SessionID)
		if _, err := os.Stat(marker); err == nil {
			return // already checked this session
		}
	}
	// Mark the session either way, so later Greps (mapped or not) skip the
	// repository scan. Errors are ignored: the hook fails open.
	isMapped := mapped(ctx, h.Cwd)
	if marker != "" {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(marker, nil, 0o644)
		cleanup(dir)
	}
	if !isMapped {
		return
	}
	event := h.Event
	if event == "" {
		event = "PreToolUse"
	}
	json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": Nudge},
	})
}

// cleanup removes session markers older than two days.
func cleanup(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 48*time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
