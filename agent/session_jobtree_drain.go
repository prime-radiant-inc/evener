package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// drainRecheckInterval bounds how long DrainJobTree blocks between explicit
// completion wakes. The outstanding count is race-free, so a re-check can never
// return prematurely; this ticker is only a lost-wake backstop and the cadence
// at which the drain re-kicks drive-down for any stranded descendant, so it is
// kept well above the completion-wake rate to avoid re-scanning the durable log
// under jm.mu more often than necessary.
const drainRecheckInterval = 250 * time.Millisecond

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
//
// The durable snapshot and the running-map read are taken under the SAME jm.mu
// hold. armFinalizedJob deletes from the running map under jm.mu and appends
// EventJobNotificationPending before that delete, so while this holds jm.mu a
// finalizing delegate is either still in the running map (delete blocked) or its
// NotifyPending record is already durable (delete done, its earlier append
// visible to Load). Loading the store outside the lock would reopen the
// stale-snapshot window. Store never acquires jm.mu, so jm.mu -> store.mu is a
// consistent order with no inverse. A store read error is returned, never
// folded into a zero count: an unreadable store is not quiescence.
func (jm *jobManager) outstandingDelegateCount() (int, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	recs, err := jm.store.Load()
	if err != nil {
		return 0, err
	}
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
	return n, nil
}

// treeHasOutstandingWork reports whether this session or any live descendant in
// its delegate subtree still owes drain work: an outstanding delegate job, a
// pending job notification, or an in-flight drive turn. Close() cancels the
// whole subtree, so the one-shot drain must settle all of it — a root whose
// direct delegate finished after spawning its own fire-and-return delegate is
// not quiescent until that descendant's work drains too. A store read error is
// treated conservatively as outstanding (keep waiting), never as quiescence.
func (s *Session) treeHasOutstandingWork() bool {
	if s.jobManager != nil {
		n, err := s.jobManager.outstandingDelegateCount()
		if err != nil || n > 0 {
			return true
		}
	}
	if s.peekNotifications() > 0 {
		return true
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		driving := sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if driving {
			return true
		}
		if child != nil && child.treeHasOutstandingWork() {
			return true
		}
	}
	return false
}

// DrainJobTree keeps re-driving the coordinator on delegate completions until no
// delegate anywhere in the subtree still owes a notification turn, then returns
// the last notification turn's result (empty when no drain turn ran). It is the
// one-shot analogue of the serve loop's notification pump: a coordinator that
// fires a fire-and-return delegate (max_wait_ms=0) ends its turn while the child
// is still running, so without this drain the caller's Close() would SIGKILL the
// child (and any descendant) before it finishes (PRI-2441).
//
// The wait is bounded by ctx: a subtree whose delegated work never completes
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
			// A completion is queued on this (root) rail: run a notification turn so
			// the coordinator's model receives it and can dispatch more work or wrap
			// up. The turn's boundary also drives any idle descendant that has
			// undelivered attention. The turn's internal loop drains any further
			// already-pending notifications.
			res, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
			if err != nil {
				return lastResult, err
			}
			if res != "" {
				lastResult = res
			}
			continue
		}
		if !s.treeHasOutstandingWork() {
			// Nothing pending and nothing outstanding anywhere in the subtree.
			return lastResult, nil
		}
		// Work is still in flight in the subtree but this rail has not been
		// signalled yet. Kick drive-down so a descendant that completed while this
		// session was idle is not stranded, then block until a completion wakes us,
		// the periodic re-check fires, or the caller's context is cancelled.
		s.driveChildrenWithUndeliveredAttention()
		select {
		case <-wake:
		case <-ticker.C:
		case <-ctx.Done():
			return lastResult, ctx.Err()
		}
	}
}
