package project

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The Elixir extractor proves library calls from the Hex packages a
// member's mix.lock files pin, read from the Hex cache (map-coverage-2).
// The facts therefore depend on which tarballs the cache holds, so the
// inputs digest records each pinned package as present (with its size) or
// missing: filling the cache rebuilds the map. The extractor itself checks
// each tarball's SHA-256 against the lock; this only notices change.

// hexEntry is one `:hex` package a lock pins.
type hexEntry struct {
	Name, Version, Outer string
}

// hexLine matches a lock entry: "key": {:hex, :name, "version", "inner",
// [managers], [deps], "hexpm", "outer"}. Entries are one line each.
var hexLine = regexp.MustCompile(`\{:hex, :([a-z0-9_]+), "([^"]+)",.*"hexpm", "([0-9a-fA-F]{64})"`)

// parseHexLock lists a lock's `:hex` entries, as data.
func parseHexLock(text []byte) []hexEntry {
	var out []hexEntry
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if m := hexLine.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, hexEntry{Name: m[1], Version: m[2], Outer: strings.ToLower(m[3])})
		}
	}
	return out
}

// hexCacheDir is Hex's package cache: $HEX_HOME/packages/hexpm, else
// ~/.hex/packages/hexpm.
func hexCacheDir() string {
	home := os.Getenv("HEX_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".hex")
	}
	return filepath.Join(home, "packages", "hexpm")
}

// extractsElixir reports whether a member runs the Elixir extractor.
func extractsElixir(in Input) bool {
	if in.Settings == nil {
		return false
	}
	for _, e := range in.Settings.Extract {
		if e == "elixir" {
			return true
		}
	}
	return false
}

// lockPaths lists the mix.lock files of a member at its commit (or in its
// working copy for a --head build), skipping deps/ and _build/.
func lockPaths(ctx context.Context, in Input) []string {
	var names []string
	if in.Head {
		filepath.WalkDir(in.Path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == "deps" || d.Name() == "_build" || d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == "mix.lock" {
				if rel, err := filepath.Rel(in.Path, p); err == nil {
					names = append(names, filepath.ToSlash(rel))
				}
			}
			return nil
		})
	} else {
		out, err := exec.CommandContext(ctx, "git", "-C", in.Path, "ls-tree", "-r", "--name-only", in.Commit).Output()
		if err != nil {
			return nil
		}
		for _, l := range strings.Split(string(out), "\n") {
			if path.Base(l) == "mix.lock" && !strings.HasPrefix(l, "deps/") && !strings.Contains(l, "/deps/") {
				names = append(names, l)
			}
		}
	}
	sort.Strings(names)
	return names
}

// hexDigestLines writes, for each pinned package of a member running the
// Elixir extractor, whether the Hex cache holds its tarball, and its size.
func hexDigestLines(ctx context.Context, w io.Writer, in Input) {
	if !extractsElixir(in) || in.Path == "" {
		return
	}
	cache := hexCacheDir()
	for _, lock := range lockPaths(ctx, in) {
		var text []byte
		var err error
		if in.Head {
			text, err = os.ReadFile(filepath.Join(in.Path, filepath.FromSlash(lock)))
		} else {
			text, err = ReadAt(ctx, in.Path, in.Commit, lock)
		}
		if err != nil {
			continue
		}
		for _, e := range parseHexLock(text) {
			state := "missing"
			if fi, err := os.Stat(filepath.Join(cache, e.Name+"-"+e.Version+".tar")); err == nil && fi.Mode().IsRegular() {
				state = fmt.Sprintf("present %d", fi.Size())
			}
			fmt.Fprintf(w, "  hex %s %s %s %s %s\n", lock, e.Name, e.Version, e.Outer, state)
		}
	}
}
