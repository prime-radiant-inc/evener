package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// drainRecheckInterval bounds how long DrainJobTree blocks between explicit
// completion wakes. The outstanding count is race-free, so a re-check can never
// return prematurely; this ticker is only a lost-wake backstop.
const drainRecheckInterval = 50 * time.Millisecond

// outstandingDelegateCount reports how many delegate jobs still owe this session
// a completion notification turn. A delegate counts while it is either still in
// the running map (not yet finalized) or recorded terminal with an owner
// notification that is pending but not yet delivered.
//
// Both halves are needed to be race-free against the finalization sequence
// (jobs.go armFinalizedJob): the running-map membership covers the window from
// EventJobFinished until the durable EventJobNotificationPending is written; the
// NotifyPending record covers the later window after the job is deleted from the
// running map but before its in-memory notification is enqueued. A suppressed
// (watch-origin) notification never reaches NotifyPending, so it correctly stops
// counting once the job leaves the running map — the drain must not wait on a
// notification that will never arrive. Background shell jobs are excluded
// entirely: a one-shot run must not block on a long-lived shell.
func (jm *jobManager) outstandingDelegateCount() int {
	recs, err := jm.store.Load()
	if err != nil {
		recs = nil
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	counted := make(map[string]bool)
	n := 0
	for id, run := range jm.running {
		if run.rec != nil && run.rec.Type == jobstore.JobDelegate {
			counted[id] = true
			n++
		}
	}
	for id, rec := range recs {
		if counted[id] {
			continue
		}
		if rec.Type == jobstore.JobDelegate && rec.NotifyState == jobstore.NotifyPending {
			n++
		}
	}
	return n
}

// DrainJobTree keeps re-driving the coordinator on delegate completions until no
// delegate still owes a notification turn, then returns the last notification
// turn's result (empty when no drain turn ran). It is the one-shot analogue of
// the serve loop's notification pump: a coordinator that fires a fire-and-return
// delegate (max_wait_ms=0) ends its turn while the child is still running, so
// without this drain the caller's Close() would SIGKILL the child before it
// finishes (PRI-2441).
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
		if s.jobManager.outstandingDelegateCount() == 0 {
			// Nothing pending and no delegate still owes a notification: quiesced.
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
