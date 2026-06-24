//go:build !race

package agent

import "time"

// testCloseGrace is small without the race detector: jobs finalize in
// milliseconds, so a short graceful-shutdown window keeps teardown fast.
const testCloseGrace = 200 * time.Millisecond
