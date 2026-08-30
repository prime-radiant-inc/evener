//go:build linux || darwin

package plugins

import (
	"os"
	"syscall"
)

// terminateGit asks a running git to exit gracefully. SIGTERM lets git's
// signal handlers remove its own lock files (.git/index.lock etc.), so a
// canceled operation cannot wedge a persistent clone the way SIGKILL can.
func terminateGit(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
