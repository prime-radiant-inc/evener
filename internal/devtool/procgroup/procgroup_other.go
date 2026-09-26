//go:build !linux && !darwin

// This platform has no process groups; the dev tooling only runs on the
// repo's unix development machines, so these stand-ins just keep the build
// green. Both stop paths collapse to a best-effort kill of the direct child,
// through the shared primitives in execsupport/procgroup.
package procgroup

import (
	"os"
	"os/exec"
	"time"

	baseprocgroup "primeradiant.com/evener/execsupport/procgroup"
)

func Start(cmd *exec.Cmd) error { return cmd.Start() }

func Terminate(pgid int) { Kill(pgid) }

func Kill(pgid int) { baseprocgroup.Kill(pgid) }

func Stop(pgid int, reaped <-chan struct{}, grace time.Duration) {
	Terminate(pgid)
	select {
	case <-reaped:
	case <-time.After(grace):
	}
}

func ExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
