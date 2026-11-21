//go:build unix

package sandbox

import (
	"context"
	"testing"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

// TestProbeOutputBoundsADescendantHoldingThePipe proves the bound structurally:
// the launcher leaves a grandchild holding the captured stdout, and the probe
// call must return while that grandchild still lives. Without cmd.WaitDelay the
// call waits for the grandchild, and Await fails after its tripwire instead of
// passing on a stopwatch.
func TestProbeOutputBoundsADescendantHoldingThePipe(t *testing.T) {
	t.Parallel()
	holder := orphanpipetest.New(t)
	script := holder.WriteScript(t, "go", "echo /usr/local/go\n"+holder.Spawn())

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := (hostProbeSystem{}).output(context.Background(), script)
		done <- result{out: out, err: err}
	}()
	got := orphanpipetest.Await(t, holder, done)
	if got.err != nil {
		t.Fatalf("output: %v", got.err)
	}
	if text := string(trimTrailingNewline(got.out)); text != "/usr/local/go" {
		t.Errorf("output = %q, want the launcher's own answer", text)
	}
}
