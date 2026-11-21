package project_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
)

func (x *h) installs(spec string) int {
	b, _ := os.ReadFile(x.miseLog)
	return strings.Count(string(b), "install "+spec+"\n")
}

// A tool whose install fails fails its inputs, and those inputs are held
// like any other failure (A-M6): the automatic cycles of the next two
// cadence intervals don't run the installer again.
func TestFailedInstallIsHeld(t *testing.T) {
	x := newH(t)
	_, sales, _ := x.world()
	x.push("sales", map[string]string{".tool-versions": "erlang 28.3.1\nelixir 1.19.9-otp-28\n"})
	x.register(sales)
	r := x.runner()
	if _, err := r.Cycle(context.Background(), "shop", false); err == nil || !strings.Contains(err.Error(), "tools") {
		t.Fatalf("want a tools failure, got %v", err)
	}
	spec := "elixir@1.19.9-otp-28"
	n := x.installs(spec)
	if n != 1 {
		t.Errorf("the failed cycle ran the installer %d times, want once", n)
	}
	for i := range 2 {
		x.advance(project.Cadence + time.Minute)
		res, err := r.Cycle(context.Background(), "shop", false)
		if err != nil || res.Imported {
			t.Fatalf("cycle %d after the failure: %+v %v", i+2, res, err)
		}
		if got := x.installs(spec); got != n {
			t.Fatalf("cycle %d after the failure ran the installer again (%d installs, was %d); skipped: %q", i+2, got, n, res.Skipped)
		}
	}
}
