package export

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/server"
	"github.com/shermanhuman/cartographer/internal/store"
)

const snap = "snapshot-20261015T120000Z"

func setup(t *testing.T) (*query.Q, http.Handler) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Import(context.Background(), "../../testdata/workspace-mini/snapshot"); err != nil {
		t.Fatal(err)
	}
	q, err := query.New(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	return q, server.New(server.Options{Store: s, Version: "test"})
}

func needChrome(t *testing.T) {
	t.Helper()
	if _, err := FindChrome(); err != nil {
		t.Skipf("skipping browser export: %v", err)
	}
}

func TestFragment(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	for _, c := range []struct {
		t    Target
		want map[string]string
	}{
		{Target{Around: "Shop.Orders.total/1"}, map[string]string{"view": "entity", "entity": "shop-app:Shop.Orders.total/1", "tab": "code"}},
		{Target{Around: "Shop.Billing.charge/2", Depth: 2}, map[string]string{"entity": "shop-app:Shop.Billing.charge/2", "depth": "2"}},
		{Target{Context: "Shop.Orders"}, map[string]string{"view": "entity", "entity": "ctx:shop-app:Shop.Orders"}},
		{Target{Context: "shop-worker:Worker.Nightly"}, map[string]string{"entity": "ctx:shop-worker:Worker.Nightly"}},
		{Target{Table: "orders"}, map[string]string{"entity": "public.orders", "tab": "data"}},
		{Target{Annotation: "checkout"}, map[string]string{"view": "checkout"}},
		{Target{Annotation: "ann:checkout"}, map[string]string{"view": "checkout"}},
	} {
		frag, err := Fragment(ctx, q, c.t)
		if err != nil {
			t.Fatalf("%+v: %v", c.t, err)
		}
		v, _ := url.ParseQuery(frag)
		for k, want := range c.want {
			if v.Get(k) != want {
				t.Errorf("%+v: %s=%q, want %q (%s)", c.t, k, v.Get(k), want, frag)
			}
		}
	}
	for _, bad := range []Target{{}, {Around: "x", Table: "y"}, {Table: "Shop.Billing"}, {Annotation: "nope"}, {Around: "zzzzzz"}} {
		if _, err := Fragment(ctx, q, bad); err == nil {
			t.Errorf("%+v: want error", bad)
		}
	}
}

func TestNoChromeHint(t *testing.T) {
	t.Setenv("CHROME_PATH", "/nonexistent/chrome")
	_, err := FindChrome()
	if !errors.Is(err, ErrNoChrome) || !strings.Contains(err.Error(), "CHROME_PATH") {
		t.Fatalf("FindChrome: %v", err)
	}
	_, h := setup(t)
	if _, err := Run(context.Background(), h, "view=home", Options{Format: "pdf"}); !errors.Is(err, ErrNoChrome) {
		t.Errorf("Run without Chrome: %v", err)
	}
	if _, err := Run(context.Background(), h, "view=home", Options{Format: "png"}); err == nil {
		t.Error("bad format accepted")
	}
}

func TestExportSVG(t *testing.T) {
	needChrome(t)
	q, h := setup(t)
	frag, err := Fragment(context.Background(), q, Target{Around: "Shop.Orders.total/1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Run(context.Background(), h, frag, Options{Format: "svg", Mode: "snippet"})
	if err != nil {
		t.Fatal(err)
	}
	svg := string(out)
	if err := xml.NewDecoder(bytes.NewReader(out)).Decode(new(struct{})); err != nil {
		t.Fatalf("SVG is not well-formed XML: %v\n%s", err, svg)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		">total/1</text>", ">charge/2</text>", ">test_total/1</text>", // focus and both callers
		`marker-end="url(#arrow)"`, "stroke:", // live edges with inlined styles
		"map " + snap, "shop-app@aaaaaaa shop-worker@bbbbbbb", "generated 20", // footer
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG lacks %q", want)
		}
	}
	if n := strings.Count(svg, `<rect width="235"`); n != 3 {
		t.Errorf("want 3 nodes, got %d", n)
	}

	jfrag, _ := Fragment(context.Background(), q, Target{Annotation: "checkout"})
	if _, err := Run(context.Background(), h, jfrag, Options{Format: "svg"}); err == nil || !strings.Contains(err.Error(), "no graph") {
		t.Errorf("journey SVG: %v", err)
	}
}

// pdfPages counts pages with a pure-Go PDF reader. Text extraction is not
// used: Chrome embeds CID fonts that the reader cannot map back to text, so
// provenance is checked in the document title (set by the print view), which
// Chrome stores uncompressed in the PDF Info dictionary.
func pdfPages(t *testing.T, b []byte) int {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a readable PDF: %v", err)
	}
	return r.NumPage()
}

func TestExportPDF(t *testing.T) {
	needChrome(t)
	q, h := setup(t)
	for _, c := range []struct {
		target Target
		mode   string
	}{
		{Target{Around: "Shop.Orders.total/1"}, "snippet"},
		{Target{Table: "public.orders"}, "page"},
		{Target{Annotation: "checkout"}, "page"},
	} {
		frag, err := Fragment(context.Background(), q, c.target)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Run(context.Background(), h, frag, Options{Format: "pdf", Mode: c.mode})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(out, []byte("%PDF-")) {
			t.Fatalf("%+v: not a PDF", c.target)
		}
		if pages := pdfPages(t, out); pages < 1 {
			t.Errorf("%+v: %d pages", c.target, pages)
		}
		for _, want := range []string{"cartographer map " + snap, "shop-app@aaaaaaa"} {
			if !bytes.Contains(out, []byte(want)) {
				t.Errorf("%+v (%s): PDF lacks %q", c.target, c.mode, want)
			}
		}
	}
}
