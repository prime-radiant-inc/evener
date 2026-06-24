//go:build race

package agent

import "time"

// testCloseGrace is generous under the race detector. Finalization runs ~10x
// slower there, so a short grace would let closeRuntimeState abandon a job that
// is still finalizing (its done channel not yet closed) and report a false
// "close timed out" — observed only on slower CI, not on fast dev machines.
const testCloseGrace = 3 * time.Second
