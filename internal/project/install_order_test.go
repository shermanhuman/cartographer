package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fresh Elixir install runs erl, so Erlang is installed first. Elixir's
// install names Erlang too (newer mise leaves its install dirs off the PATH
// the install's check sees, so mise must install the pair together), and
// Erlang's bin dir is ahead of PATH for older mise.
func TestElixirInstallFindsErlang(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	// A stand-in mise: nothing is installed until `install`; `install` logs
	// its tools and the first PATH entry; bin-paths prints <dir>/<name>/bin.
	script := `#!/bin/sh
state=` + dir + `
case "$1" in
where) [ -e "$state/installed-$2" ] || exit 1; echo "$state/$2";;
install) shift; echo "$* ${PATH%%:*}" >> ` + log + `; for t in "$@"; do touch "$state/installed-$t"; done;;
bin-paths) n=${2%%@*}; mkdir -p "$state/$n/bin"; echo "$state/$n/bin";;
env) echo '{}';;
esac
`
	mise := filepath.Join(dir, "mise")
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Mise{Bin: mise, Dir: dir}
	_, err := m.Env(context.Background(), []Tool{{"elixir", "1.19.5-otp-28"}, {"erlang", "28.3.1"}})
	// Env then checks each tool's programs resolve in its bins, which the
	// stand-in doesn't provide; only the install order matters here.
	_ = err
	raw, rerr := os.ReadFile(log)
	if rerr != nil {
		t.Fatal(rerr)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "erlang@28.3.1 ") || !strings.HasPrefix(lines[1], "erlang@28.3.1 elixir@1.19.5-otp-28 "+filepath.Join(dir, "erlang", "bin")) {
		t.Errorf("installs:\n%s", raw)
	}
}
