// Package plugins embeds the bundled extractor plugins so a single
// cartographer binary can run builds without a source checkout.
package plugins

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed extract-elixir/*.exs extract-elixir/README.md
//go:embed extract-javascript/*.mjs extract-javascript/package.json extract-javascript/README.md extract-javascript/vendor
//go:embed extract-postgres-schema/*.exs extract-postgres-schema/schema.sql extract-postgres-schema/README.md
var FS embed.FS

// digest identifies this set of embedded files.
func digest() string {
	h := sha256.New()
	fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := FS.ReadFile(p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Extract writes the embedded plugins under cacheDir/plugins/<digest> (once)
// and returns that directory, for use as {plugins} in plugin commands. Each
// call extracts into its own temporary directory and renames it into place,
// so concurrent calls never share files; a complete directory is never
// removed or replaced.
func Extract(cacheDir string) (string, error) {
	dir := filepath.Join(cacheDir, "plugins", digest())
	if complete(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // a no-op once renamed
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	err = fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another call won the race: use its directory.
		if complete(dir) {
			return dir, nil
		}
		// Renamed directories are always complete, so this is a leftover of
		// an older version. Never remove what is there.
		return "", fmt.Errorf("plugins: %s exists but is incomplete (remove it): %w", dir, err)
	}
	return dir, nil
}

func complete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".complete"))
	return err == nil
}
