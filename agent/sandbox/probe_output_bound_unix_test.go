//go:build unix

package sandbox

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestProbeOutputBoundsADescendantHoldingThePipe: a launcher that leaves a
// background descendant holding the captured stdout must not stall a probe past
// probeWaitDelay. exec waits for the pipe to close, and the descendant holds it
// after the direct child exits, so without cmd.WaitDelay Output returns only
// when the descendant does (here, after twenty seconds).
func TestProbeOutputBoundsADescendantHoldingThePipe(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh is unavailable: %v", err)
	}
	start := time.Now()
	out, err := (hostProbeSystem{}).output(context.Background(), "/bin/sh", "-c", "echo /usr/local/go; sleep 20 &")
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if got := string(trimTrailingNewline(out)); got != "/usr/local/go" {
		t.Errorf("output = %q, want the child's own answer", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("output waited %s for a background descendant; probeWaitDelay must bound it", elapsed)
	}
}
