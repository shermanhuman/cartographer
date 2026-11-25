package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shermanhuman/cartographer/internal/query"
	cachesession "github.com/shermanhuman/cartographer/internal/session"
)

// An agent in a project checkout with no map yet is told that project's own
// project add, not the generic owner/name advice (decision 6).
func TestNoMapNamesTheCheckoutsProject(t *testing.T) {
	ctx := context.Background()
	co := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", co).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	os.MkdirAll(filepath.Join(co, ".cartographer"), 0o755)
	os.WriteFile(filepath.Join(co, ".cartographer", "repo.toml"), []byte("project = \"app\"\nanchor = \"acme/app-admin\"\n"), 0o644)
	noMap := &cachesession.ErrNoMap{Cwd: co, StateDir: t.TempDir()}
	server := New(Options{Version: "test", Prepare: func(context.Context) (*query.Q, error) { return nil, noMap }, Extra: EmbedTool})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tool := range []struct {
		name string
		args map[string]any
	}{{"map_status", map[string]any{}}, {"map_embed", map[string]any{"ref": "x"}}} {
		out, isErr := call(t, cs, tool.name, tool.args)
		if !isErr || !strings.Contains(out, "project add acme/app-admin") {
			t.Errorf("%s: got (%v) %s", tool.name, isErr, out)
		}
	}
}
