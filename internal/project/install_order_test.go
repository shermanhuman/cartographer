package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fresh Elixir install runs erl, and mise keeps its own install dirs off
// the PATH that check sees, so Erlang is installed first and Elixir's
// install names it too, at the version mise has installed: a prefix pin
// (erlang 28) must not make the pair install a newer 28.x.
func TestElixirInstallNamesErlang(t *testing.T) {
	for _, tc := range []struct{ name, erlang, want string }{
		{"exact pin", "28.3.1", "erlang@28.3.1 elixir@1.19.5-otp-28"},
		{"prefix pin", "28", "erlang@28.3.1 elixir@1.19.5-otp-28"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "log")
			// A stand-in mise: nothing is installed until `install`, which
			// logs its tools; erlang@28 installs and resolves as 28.3.1;
			// bin-paths prints <dir>/<name>/bin.
			script := `#!/bin/sh
state=` + dir + `
norm() { case "$1" in erlang@28) echo erlang@28.3.1;; *) echo "$1";; esac; }
case "$1" in
where) [ -e "$state/installed-$(norm "$2")" ] || exit 1; echo "$state/$2";;
install) shift; echo "$*" >> ` + log + `; for t in "$@"; do touch "$state/installed-$(norm "$t")"; done;;
latest) [ -e "$state/installed-$(norm "$3")" ] || exit 1; v=$(norm "$3"); echo "${v#*@}";;
bin-paths) n=${2%%@*}; mkdir -p "$state/$n/bin"; echo "$state/$n/bin";;
env) echo '{}';;
esac
`
			mise := filepath.Join(dir, "mise")
			if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			m := &Mise{Bin: mise, Dir: dir}
			if err := m.Install(context.Background(), []Tool{{"elixir", "1.19.5-otp-28"}, {"erlang", tc.erlang}}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 2 || lines[0] != "erlang@"+tc.erlang || lines[1] != tc.want {
				t.Errorf("installs:\n%s", raw)
			}
		})
	}
}
