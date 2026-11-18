//go:build !windows

package mergeq

import (
	"context"
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
	t.Parallel()
	h := orphanpipetest.New(t)
	gh := h.WriteScript(t, "gh", "printf '{\"data\":{}}'\n"+h.Spawn()+"\n")
	done := make(chan struct {
		out []byte
		err error
	}, 1)
	go func() {
		out, err := (GHClient{GH: gh}).run(context.Background(), "api", "graphql")
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
}
