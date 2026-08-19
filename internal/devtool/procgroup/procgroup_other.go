//go:build !linux && !darwin

// This platform has no process groups; the dev tooling only runs on the
// repo's unix development machines, so these stand-ins just keep the build
// green (mirroring cmd/evener-test-dev-tooling's wave_other.go). Both stop
// paths collapse to a best-effort kill of the direct child.
package procgroup

import (
	"os"
	"os/exec"
	"time"
)

func Start(cmd *exec.Cmd) error { return cmd.Start() }

func Terminate(pgid int) { Kill(pgid) }

func Kill(pgid int) {
	if pgid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pgid); err == nil {
		_ = proc.Kill()
	}
}

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
