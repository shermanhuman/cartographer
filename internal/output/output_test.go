package output

import (
	"strings"
	"testing"
)

func TestRenderCutsAtBudgetWithNote(t *testing.T) {
	d := &Doc{}
	d.H("header")
	for i := 0; i < 100; i++ {
		d.L("line %02d %s", i, strings.Repeat("x", 30))
	}
	d.F("footer")
	out := d.Render(100) // 400 chars
	if len(out) > 400 {
		t.Errorf("len = %d > 400", len(out))
	}
	if !strings.HasPrefix(out, "header\nline 00") {
		t.Errorf("header/first line missing:\n%s", out)
	}
	if !strings.Contains(out, "more lines (raise budget") || !strings.HasSuffix(out, "footer\n") {
		t.Errorf("truncation note or footer missing:\n%s", out)
	}
}

func TestRenderFitsWithoutNote(t *testing.T) {
	d := &Doc{}
	d.L("a")
	d.L("b")
	if out := d.Render(0); out != "a\nb\n" {
		t.Errorf("got %q", out)
	}
}
