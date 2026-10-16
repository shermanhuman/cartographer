package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Flag groups shared by several commands.
const (
	queryFlags = "db snapshot no-pull budget format"
	pagedFlags = queryFlags + " limit offset"
)

// commandFlags lists every command and the flags that apply to it ("*"
// means all flags), for validation and `<command> --help`.
var commandFlags = map[string]string{
	"status": queryFlags, "show": queryFlags, "entity": queryFlags,
	"path": queryFlags, "table": queryFlags, "context": queryFlags,
	"find":    pagedFlags,
	"callers": pagedFlags + " depth", "callees": pagedFlags + " depth", "impact": pagedFlags + " depth",
	"mermaid": "db snapshot no-pull depth max",
	"embed":   "db snapshot no-pull",
	"diff":    "db",
	"export":  "db snapshot no-pull depth pdf svg page snippet around context table annotation o",
	"serve":   "db snapshot no-pull port open",
	"pull":    "db", "workspace": "", "import": "db", "snapshots": "db",
	"mapped": "db no-pull", "hook": "db no-pull", "mcp": "db snapshot no-pull",
	"init": "", "build": "*", "publish": "workspace", "review": "db snapshot workspace",
	"validate-facts": "", "plugins-dir": "", "version": "",
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
		if names != "*" && !slices.Contains(want, f.Name) {
			return
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			def = fmt.Sprintf(" (default %s)", f.DefValue)
		}
		fmt.Fprintf(w, "  -%s\t%s%s\n", f.Name, f.Usage, def)
	})
}

var pluginRef = regexp.MustCompile(`\{plugins\}[/\\]([^/\\]+)`)

// schemaPluginsDigest identifies the bundled plugin code a schema command
// runs (the {plugins}/<dir> it names), so editing another plugin does not
// invalidate the schema cache.
func schemaPluginsDigest(command []string, pluginsDir string) (string, error) {
	var dirs []string
	for _, arg := range command {
		for _, m := range pluginRef.FindAllStringSubmatch(arg, -1) {
			if !slices.Contains(dirs, m[1]) {
				dirs = append(dirs, m[1])
			}
		}
	}
	slices.Sort(dirs)
	h := sha256.New()
	fmt.Fprintf(h, "schema-plugins:%q\n", dirs)
	for _, d := range dirs {
		sum, err := workspace.DirDigest(filepath.Join(pluginsDir, d))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("build: schema plugin digest: %w", err)
		}
		fmt.Fprintf(h, "%s=%s\n", d, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
