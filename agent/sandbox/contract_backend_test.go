package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

// TestContractRealizedAgainstBwrap realizes the M1 golden contract against the
// REAL bwrap backend: for every sandboxed mode × net cell the contract says bwrap
// serves, it resolves against real host facts and a real git workspace, confirms
// the resolver picks BackendBwrap, then builds a live wrapper and runs a trivial
// command under it — proving the backend actually enforces the cell (PID-ns
// isolation holds; the command runs). This is the M3 counterpart to
// TestContractMatrix, which checks the resolver in the abstract.
func TestContractRealizedAgainstBwrap(t *testing.T) {
	facts := requireRealBwrap(t)

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		for _, netOn := range []bool{true, false} {
			name := fmt.Sprintf("%s/net-%v", mode, netOn)
			t.Run(name, func(t *testing.T) {
				f := facts
				f.Home = t.TempDir()
				cwd := MaterializeWorkspace(t, MainCheckout)

				net := netOn
				rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, f, cwd)
				if err != nil {
					t.Fatalf("Resolve(%s, net=%v): %v", mode, netOn, err)
				}
				if rp.Backend != BackendBwrap {
					t.Fatalf("contract cell %s must resolve to bwrap, got %v", name, rp.Backend)
				}

				out, err := runWrapped(t, f, mode, netOn, cwd, t.TempDir(),
					`echo READY; echo comm=$(cat /proc/1/comm)`)
				if err != nil {
					t.Fatalf("confined command failed for %s: %v\n%s", name, err, out)
				}
				if !strings.Contains(out, "READY") {
					t.Errorf("%s: confined command did not run:\n%s", name, out)
				}
				if !strings.Contains(out, "comm=bwrap") {
					t.Errorf("%s: PID-ns isolation not in effect (PID 1 != bwrap):\n%s", name, out)
				}
			})
		}
	}
}
