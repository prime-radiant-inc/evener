package execenv

import "time"

// Shrink the SIGTERM->SIGKILL grace for the whole test binary so tests that
// exercise process termination do not pay the production 2s dead-wait.
func init() {
	terminateGrace = 200 * time.Millisecond
}
