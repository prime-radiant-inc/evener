//go:build !unix

package plugins

import "os"

// terminateGit kills git outright: non-unix platforms (windows, plan9) have
// no SIGTERM delivery, so prompt termination beats waiting out exec's
// WaitDelay for a signal that cannot arrive.
func terminateGit(p *os.Process) error { return p.Kill() }
