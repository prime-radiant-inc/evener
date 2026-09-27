package procgroup

import "testing"

// The guards must be silent no-ops for pids that can never name a group, so a
// stale or failed spawn cannot signal the caller's own process group.
func TestGuardsIgnoreNonPositivePids(t *testing.T) {
	Terminate(0)
	Terminate(-1)
	Kill(0)
	Kill(-42)
}
