package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/facts"
)

func TestExtractHelpAndUsage(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"extract", "--help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cartographer extract scip --lang go|rust", "-lang", "-repo", "-root", "-revision", "-rust-build-scripts"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, out.String())
		}
	}
	if err := run(context.Background(), []string{"extract", "bogus"}, &out); err == nil || !strings.Contains(err.Error(), "usage: cartographer extract scip|kubernetes|urls") {
		t.Errorf("extract bogus: %v", err)
	}
	if err := run(context.Background(), []string{"extract", "scip", "--lang", "cobol", "--repo", "x"}, &out); err == nil {
		t.Error("an unknown language must fail")
	}
}

// With scip-go on PATH, extract prints one valid facts v2 document.
func TestExtractScipGo(t *testing.T) {
	for _, n := range []string{"scip-go", "go"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not on PATH", n)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"extract", "scip", "--lang", "go", "--repo", "stats", "--root", "../../testdata/scip-go-stats", "--revision", "abc"}, &out); err != nil {
		t.Fatal(err)
	}
	d, err := facts.Decode(&out)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil || d.FactsVersion != 2 || d.Repo != "stats" || d.Revision != "abc" || len(d.Stats) != 1 || d.Stats[0].CallSites != 2 {
		t.Errorf("document %+v, %v", d, err)
	}
}
