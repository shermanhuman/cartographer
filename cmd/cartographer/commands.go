package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Flag groups shared by several commands.
const (
	queryFlags = "db snapshot no-pull no-rebuild budget format"
	pagedFlags = queryFlags + " limit offset"
)

// commandFlags lists every command and the flags that apply to it, for
// validation and `<command> --help`.
var commandFlags = map[string]string{
	"status": queryFlags, "show": queryFlags, "entity": queryFlags,
	"path": queryFlags, "table": queryFlags, "context": queryFlags,
	"find":    pagedFlags,
	"callers": pagedFlags + " depth", "callees": pagedFlags + " depth", "impact": pagedFlags + " depth",
	"mermaid": "db snapshot no-pull no-rebuild depth max",
	"embed":   "db snapshot no-pull no-rebuild",
	"diff":    "db",
	"export":  "db snapshot no-pull no-rebuild depth pdf svg page snippet around context table annotation o",
	"serve":   "db snapshot no-pull no-rebuild port open",
	"pull":    "db", "workspace": "", "import": "db", "snapshots": "db",
	"mapped": "db no-pull", "hook": "db no-pull", "mcp": "db snapshot no-pull no-rebuild",
	"init": "", "build": "db workspace out plugins import local background fetch fetch-only ref", "publish": "workspace", "review": "db snapshot workspace",
	"validate-facts": "", "plugins-dir": "", "version": "",
	"extract": "lang repo root revision rust-build-scripts cargo-target-dir",
	"project": "db plugins yes project anchor", "refresh": "db plugins head from background",
}

// jsonCommands are the queries that --format json applies to.
var jsonCommands = []string{"status", "find", "show", "entity", "callers", "callees", "impact", "path", "table", "context"}

func checkFormat(cmd, format string) error {
	switch format {
	case "text":
		return nil
	case "json":
		if slices.Contains(jsonCommands, cmd) {
			return nil
		}
		return fmt.Errorf("%s: --format json applies to %s only", cmd, strings.Join(jsonCommands, ", "))
	}
	return fmt.Errorf("%s: --format %q: want text or json", cmd, format)
}

// commandHelp prints cmd's lines from the usage text and the flags that
// apply to it.
func commandHelp(w io.Writer, fs *flag.FlagSet, cmd string) {
	fmt.Fprintln(w, "usage:")
	in := false
	for _, line := range strings.Split(usage, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "         ") && (t == cmd || strings.HasPrefix(t, cmd+" ")):
			in = true
			fmt.Fprintln(w, "  cartographer "+t)
		case in && strings.HasPrefix(line, "         "): // continuation
			fmt.Fprintln(w, line)
		default:
			in = false
		}
	}
	names := commandFlags[cmd]
	if names == "" {
		return
	}
	fmt.Fprintln(w, "\nflags:")
	want := strings.Fields(names)
	fs.VisitAll(func(f *flag.Flag) {
		if !slices.Contains(want, f.Name) {
			return
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			def = fmt.Sprintf(" (default %s)", f.DefValue)
		}
		fmt.Fprintf(w, "  -%s\t%s%s\n", f.Name, f.Usage, def)
	})
}

// schemaPluginsDigest identifies the bundled plugin code a schema command
// runs (see workspace.SchemaPluginsDigest).
func schemaPluginsDigest(command []string, pluginsDir string) (string, error) {
	return workspace.SchemaPluginsDigest(command, pluginsDir)
}
