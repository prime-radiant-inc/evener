//go:build !linux && !darwin

package plugins

import "os"

// terminateGit kills git outright: this platform has no SIGTERM delivery, so
// prompt termination beats waiting out exec's WaitDelay for a signal that
// cannot arrive.
func terminateGit(p *os.Process) error { return p.Kill() }
