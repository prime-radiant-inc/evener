//go:build darwin

package agent

import (
	"time"

	"primeradiant.com/evener/execsupport/procgroup"
)

// processStartedAt reports when the process named by pid began, through
// procgroup's proc_pidinfo query (the same one the hub's daemon-identity
// verifier uses). It is the PID-reuse guard for reaping a dead daemon's
// background job groups: a signal is sent only when this time still matches the
// one the durable job record carries, so a recycled pid/pgid is never signaled.
//
// ok=false means the start time could not be established (the process is gone,
// or the kernel refused the query); callers must treat that as "not the
// recorded process", never as proof either way.
func processStartedAt(pid int) (time.Time, bool) {
	return procgroup.ProcessStartTime(pid)
}
