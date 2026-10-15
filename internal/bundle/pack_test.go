package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

func tarball(t *testing.T, h *tar.Header, body string) *bytes.Buffer {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	h.Size = int64(len(body))
	tw.WriteHeader(h)
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return &buf
}

func TestUnpackRejectsUnsafeEntries(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "/abs", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		err := Unpack(tarball(t, h, "x"), t.TempDir())
		if err == nil || !(strings.Contains(err.Error(), "unsafe") || strings.Contains(err.Error(), "unsupported")) {
			t.Errorf("%s: got %v", h.Name, err)
		}
	}
}
