package project

import (
	"errors"
	"testing"
)

func TestPluginCauseLeadsWithTheCause(t *testing.T) {
	root := "/tmp/state/build-1/work/repos/breakdown-sales"
	stderr := "/x/beam.smp: /usr/lib64/libtinfo.so.6: no version information available (required by beam)\ncartographer: scip: no go.mod under " + root + "\n"
	got := pluginCause(stderr, errors.New("/x/bin/cartographer: exit status 1"), root, "breakdown-sales").Error()
	if got != "scip: no go.mod under breakdown-sales" {
		t.Errorf("got %q", got)
	}
	if got := pluginCause("", errors.New("exit status 2"), root, "r").Error(); got != "exit status 2" {
		t.Errorf("no stderr: got %q", got)
	}
}
