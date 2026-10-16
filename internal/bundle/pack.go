package bundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxUnpacked caps the total size Unpack will write (snapshots are a few MB).
const MaxUnpacked = 512 << 20

var maxUnpacked int64 = MaxUnpacked // lowered in tests

// MaxEntries caps the number of archive entries Unpack accepts, so a flood
// of empty files can't exhaust inodes under the size cap.
const MaxEntries = 200_000

var maxEntries = MaxEntries // lowered in tests

// Pack writes dir as a deterministic .tar.gz: sorted entries, zero times,
// fixed ownership and modes, so identical snapshots pack to identical bytes.
func Pack(dir string, w io.Writer) error {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
		case d.Type().IsRegular():
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		default:
			return fmt.Errorf("bundle: %s is not a regular file", p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	gz, _ := gzip.NewWriterLevel(w, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		h := &tar.Header{Name: f, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Unpack extracts a Pack archive into dst, rejecting unsafe paths, links and
// archives larger than MaxUnpacked or with more than MaxEntries entries.
func Unpack(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := filepath.Clean(dst)
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	var total int64
	for entries := 1; ; entries++ {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if entries > maxEntries {
			return fmt.Errorf("bundle: archive has more than %d entries", maxEntries)
		}
		name, ok := cleanRel(h.Name)
		if !ok {
			return fmt.Errorf("bundle: unsafe path %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("bundle: unsupported entry %q (type %c)", h.Name, h.Typeflag)
		}
		total += h.Size
		if total > maxUnpacked {
			return fmt.Errorf("bundle: archive exceeds %d bytes", maxUnpacked)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("bundle: unsafe path %q", h.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, h.Size))
		f.Close()
		if err != nil {
			return err
		}
	}
}

// cleanRel cleans a slash-separated relative path and reports whether it
// stays inside its root: no backslashes, not absolute, no leading "..".
func cleanRel(name string) (string, bool) {
	clean := path.Clean(name)
	if strings.Contains(name, `\`) || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}
