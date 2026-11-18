//go:build !windows

package mergeq

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

// TestGHDrainIsBoundedAfterAnAnswer pins that a gh which has answered cannot hold
// the caller past the drain bound. A `gh` that is a wrapper starting the real CLI
// without `exec` hands it the captured pipe, so killing the wrapper on
// cancellation leaves the CLI holding it and Run waits for the pipe: an
// interrupted wait would exit 130 seconds late instead of at once.
// docs/developing-evener/testing.md requires cmd.WaitDelay for exactly this shape.
//
// The staging is structural, not a stopwatch: the grandchild blocks on a FIFO
// the test releases, so a call that waited for the grandchild could not return
// while it lives, and "returned anyway" is an observable fact. The answer the
// wrapper wrote must survive: only the drain timed out.
func TestGHDrainIsBoundedAfterAnAnswer(t *testing.T) {
	h := orphanpipetest.New(t)
	gh := h.WriteScript(t, "gh", "printf '{\"data\":{}}'\n"+h.Spawn()+"\n")
	// A handle in the environment, planted where a launcher running this binary
	// would have put its own. A fixture must not touch it: an unset GroupFile
	// reads EVENER_MERGEQ_GH_GROUP_FILE, so a fixture that leaves it unset
	// publishes over that file and removes it on the way out -- destroying a live
	// launcher's only handle to the gh call it may be about to have to stop.
	//
	// The variable is set for the process, not through t.Setenv, because this
	// case cannot be parallel and t.Setenv forbids it: the whole point is a
	// process-wide value a fixture inherits.
	inherited := filepath.Join(t.TempDir(), "live-launcher-handle")
	if err := os.WriteFile(inherited, []byte("4321\n"), 0o600); err != nil {
		t.Fatalf("write the launcher's handle: %v", err)
	}
	t.Setenv(ghGroupFileEnv, inherited)
	// The explicit handle, inside this case's own directory, is what keeps the
	// fixture off the inherited one.
	groupFile := filepath.Join(t.TempDir(), "gh-group")
	done := make(chan struct {
		out []byte
		err error
	}, 1)
	go func() {
		out, err := (GHClient{GH: gh, GroupFile: groupFile}).run(context.Background(), "api", "graphql")
		done <- struct {
			out []byte
			err error
		}{out, err}
	}()
	got := orphanpipetest.Await(t, h, done)
	if got.err != nil {
		t.Fatalf("run = %v, want the wrapper's answer: the answer must survive a drain that timed out", got.err)
	}
	if string(got.out) != `{"data":{}}` {
		t.Fatalf("run = %q, want the wrapper's complete answer", got.out)
	}
	body, err := os.ReadFile(inherited)
	if err != nil {
		t.Fatalf("the inherited handle %s is gone: a fixture that leaves GroupFile unset publishes over a live launcher's handle and then removes it (%v)", inherited, err)
	}
	if string(body) != "4321\n" {
		t.Fatalf("the inherited handle now reads %q, want the launcher's own group: the fixture published over another run's cancellation handle", body)
	}
}
