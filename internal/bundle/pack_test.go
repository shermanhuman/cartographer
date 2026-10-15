package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mini = "../../testdata/workspace-mini/snapshot"

func TestPackIsDeterministicAndRoundTrips(t *testing.T) {
	var a, b bytes.Buffer
	if err := Pack(mini, &a); err != nil {
		t.Fatal(err)
	}
	if err := Pack(mini, &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("pack is not deterministic")
	}
	dst := t.TempDir()
	if err := Unpack(bytes.NewReader(a.Bytes()), dst); err != nil {
		t.Fatal(err)
	}
	bun, err := Read(dst)
	if err != nil {
		t.Fatal(err)
	}
	if bun.Manifest.ID != "snapshot-20261015T120000Z" || len(bun.Facts) != 3 || len(bun.Docs) != 1 {
		t.Errorf("round trip lost data: %+v", bun.Manifest)
	}
}

type entry struct {
	h    *tar.Header
	body string
}

func tarball(t *testing.T, h *tar.Header, body string) *bytes.Buffer {
	return tarEntries(t, entry{h, body})
}

func tarEntries(t *testing.T, es ...entry) *bytes.Buffer {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		e.h.Size = int64(len(e.body))
		tw.WriteHeader(e.h)
		tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	return &buf
}

func TestUnpackRejectsUnsafeEntries(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "/abs", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: `..\..\evil`, Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: `facts\x.json`, Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: ".", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		err := Unpack(tarball(t, h, "x"), t.TempDir())
		if err == nil || !(strings.Contains(err.Error(), "unsafe") || strings.Contains(err.Error(), "unsupported")) {
			t.Errorf("%s: got %v", h.Name, err)
		}
	}
}

func TestUnpackDuplicateEntryLastWins(t *testing.T) {
	dst := t.TempDir()
	reg := func() *tar.Header { return &tar.Header{Name: "a.json", Typeflag: tar.TypeReg, Mode: 0o644} }
	if err := Unpack(tarEntries(t, entry{reg(), "first, longer"}, entry{reg(), "second"}), dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "a.json"))
	if err != nil || string(b) != "second" {
		t.Fatalf("a.json = %q, %v", b, err)
	}
}

func TestUnpackSizeCap(t *testing.T) {
	old := maxUnpacked
	maxUnpacked = 10
	t.Cleanup(func() { maxUnpacked = old })
	reg := func(n string) *tar.Header { return &tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644} }
	if err := Unpack(tarEntries(t, entry{reg("a"), "12345"}, entry{reg("b"), "12345"}), t.TempDir()); err != nil {
		t.Fatalf("at cap: %v", err)
	}
	err := Unpack(tarEntries(t, entry{reg("a"), "12345"}, entry{reg("b"), "123456"}), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 bytes") {
		t.Fatalf("over cap: got %v", err)
	}
}

func TestPackRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte("{}"), 0o644)
	if err := os.Symlink("a.json", filepath.Join(dir, "link.json")); err != nil {
		t.Skip(err)
	}
	err := Pack(dir, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("got %v", err)
	}
}
