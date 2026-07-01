package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// drainRecheckInterval bounds how long DrainJobTree blocks between explicit
// completion wakes. A finished delegate is removed from the running map only
// after its completion notification is enqueued, so the wake normally arrives
// first; this periodic re-check only backstops the microscopic window where the
// running-map delete lands without its own wake.
const drainRecheckInterval = 50 * time.Millisecond

// inFlightDelegateCount reports how many delegate jobs are still in this
// session's running map. A delegate stays there until finalization has enqueued
// its completion notification and released it (jobs.go armFinalizedJob), so a
// non-zero count means the coordinator still owes at least one notification
// turn. Background shell jobs are intentionally excluded: a one-shot run must
// not block on a long-lived shell the model left running.
func (jm *jobManager) inFlightDelegateCount() int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	n := 0
	for _, run := range jm.running {
		if run.rec != nil && run.rec.Type == jobstore.JobDelegate {
			n++
		}
	}
	return n
}

// DrainJobTree keeps re-driving the coordinator on delegate completions until no
// delegate remains in flight, then returns the last notification turn's result
// (empty when no drain turn ran). It is the one-shot analogue of the serve
// loop's notification pump: a coordinator that fires a fire-and-return delegate
// (max_wait_ms=0) ends its turn while the child is still running, so without
// this drain the caller's Close() would SIGKILL the child before it finishes
// (PRI-2441).
//
// The wait is bounded by ctx: a coordinator whose delegated work never completes
// blocks until the caller's context is cancelled. Individual delegate turns
// carry their own round/time caps, so a well-formed tree always quiesces on its
// own. Only delegate jobs hold the drain open; background shell jobs do not.
func (s *Session) DrainJobTree(ctx context.Context) (string, error) {
	if s.jobManager == nil {
		return "", nil
	}
	wake := make(chan struct{}, 1)
	s.SetNotifyFunc(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	defer s.SetNotifyFunc(nil)

	ticker := time.NewTicker(drainRecheckInterval)
	defer ticker.Stop()

	lastResult := ""
	for {
		if s.peekNotifications() > 0 {
			// A completion is queued: run a notification turn so the coordinator's
			// model receives it and can dispatch more work or wrap up. The turn's
			// internal loop drains any further already-pending notifications.
			res, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
			if err != nil {
				return lastResult, err
			}
			if res != "" {
				lastResult = res
			}
			continue
		}
		if s.jobManager.inFlightDelegateCount() == 0 {
			// Nothing pending and no delegate still finalizing: the tree quiesced.
			return lastResult, nil
		}
		// A delegate is still in flight but has not signalled yet. Block until a
		// child completion wakes us, the periodic re-check fires, or the caller's
		// context is cancelled.
		select {
		case <-wake:
		case <-ticker.C:
		case <-ctx.Done():
			return lastResult, ctx.Err()
		}
	}
}
