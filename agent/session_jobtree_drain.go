package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// drainStallTimeout bounds how long the one-shot drain will keep blocking on a
// subtree that is outstanding but has NO live or deliverable component anywhere
// (a genuine stall — see drainSubtreeIsStalled). It is a defense-in-depth
// backstop for an unknown future stranding class: in a correct build the drain
// self-heals (rematerializeDurablePendings) and this predicate is never true for
// long, so the timeout only bites a regression. It is deliberately GENEROUS —
// a stalled tree owes no live work, so waiting two minutes before giving up
// costs nothing but guarantees an otherwise-forever hang cannot survive it.
// A package var (not a const) so tests override and restore it without wall
// time, matching laneClosePassBudget in jobs.go.
var drainStallTimeout = 2 * time.Minute

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
	ids, err := jm.outstandingDelegateIDs()
	return len(ids), err
}

// outstandingDelegateIDs lists the job ids outstandingDelegateCount counts: this
// session's own delegates still in the running map, plus its own delegates
// recorded terminal with a NotifyPending owner notification. The stall watchdog
// uses the ids to name the stuck delegate(s) in its warning; the count is just
// len() of this list, so the two can never disagree.
func (jm *jobManager) outstandingDelegateIDs() ([]string, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	counted := make(map[string]bool)
	var ids []string
	for id, run := range jm.running {
		if run.rec != nil && run.rec.Type == jobstore.JobDelegate {
			counted[id] = true
			ids = append(ids, id)
		}
	}
	for id, rec := range recs {
		if counted[id] {
			continue
		}
		// Only this session's OWN delegate notifications hold the drain open. A
		// forwarded descendant copy (OwnerSessionID names a child, not this
		// session) is a drive signal for that child's attention; the child is
		// covered by the recursive tree walk — and skipped there when stop-gated.
		// Counting the forwarded copy here would hang the drain on a stop-gated
		// child that nothing will ever settle (matches the owned-records filter in
		// armPendingTerminalNotifications).
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		if rec.Type == jobstore.JobDelegate && rec.NotifyState == jobstore.NotifyPending {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// hasRunningDelegate reports whether any delegate job is still in the running
// map — a delegate that has not finalized is LIVE work (a long build, a slow
// tool), never a stall. The drain stall watchdog treats a running delegate
// anywhere in the subtree as a reason to keep waiting.
func (jm *jobManager) hasRunningDelegate() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, run := range jm.running {
		if run.rec != nil && run.rec.Type == jobstore.JobDelegate {
			return true
		}
	}
	return false
}

// treeHasOutstandingWork reports whether this session or any live descendant in
// its delegate subtree still owes drain work: an outstanding delegate job, a
// pending job notification, a pending caller-targeted watch send, or an
// in-flight drive turn. These are the same "undelivered attention" signals
// driveChildrenWithUndeliveredAttention acts on, so the drain settles exactly
// what the drive machinery can still deliver. Close() cancels the whole subtree,
// so the one-shot drain must settle all of it — a root whose direct delegate
// finished after spawning its own fire-and-return delegate is not quiescent
// until that descendant's work drains too.
//
// A store read error is propagated (not folded into quiescence): the drain must
// neither Close() on an unreadable store nor spin on it forever, so the loop
// surfaces the failure instead.
func (s *Session) treeHasOutstandingWork() (bool, error) {
	if s.jobManager != nil {
		n, err := s.jobManager.outstandingDelegateCount()
		if err != nil {
			return false, err
		}
		if n > 0 || s.jobManager.hasPendingWatchSends() {
			return true, nil
		}
	}
	if s.peekNotifications() > 0 {
		return true, nil
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		driving := sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && s.childStopGated(child.id) {
			// A deliberately stopped child is never driven — driveChildrenWithUndeliveredAttention
			// skips stop-gated children — so its pre-stop attention will never be
			// delivered. Counting it (or its subtree) would hang the drain forever,
			// so match the drive gate and skip it.
			continue
		}
		if driving {
			return true, nil
		}
		if child != nil {
			outstanding, err := child.treeHasOutstandingWork()
			if err != nil {
				return false, err
			}
			if outstanding {
				return true, nil
			}
		}
	}
	return false, nil
}

// drainSubtreeIsStalled reports whether the subtree is GENUINELY WEDGED: it
// still owes drain work (treeHasOutstandingWork) yet contains NO live or
// deliverable component anywhere. This is the only condition under which the
// stall watchdog is allowed to give up, and it is deliberately narrow so it can
// never cut legitimate work.
//
// The four live/deliverable components — any of them anywhere in the subtree
// means NOT stalled — are:
//   - a delegate still in some jm.running (hasRunningDelegate): live work such
//     as a long build produces no drain progress for minutes yet is not wedged;
//   - a driving child (sub.driving): a drive turn is in flight;
//   - a pending caller-targeted watch send (hasPendingWatchSends): deliverable;
//   - a queued notification at any level (peekNotifications > 0): deliverable now.
//
// When outstanding is true but none of those exist, the outstanding work is
// composed entirely of terminal-but-undelivered notifications that the
// drive/recheck machinery is not converting — a stranded wedge. It walks the
// same liveDirectSubagents / childStopGated path treeHasOutstandingWork does,
// skipping stop-gated children identically (they are never driven, so their
// leftover state is not a stall the drain can act on).
func (s *Session) drainSubtreeIsStalled() (bool, error) {
	outstanding, err := s.treeHasOutstandingWork()
	if err != nil {
		return false, err
	}
	if !outstanding {
		return false, nil
	}
	live, err := s.subtreeHasLiveComponent()
	if err != nil {
		return false, err
	}
	return !live, nil
}

// subtreeHasLiveComponent reports whether any live or deliverable drain
// component (see drainSubtreeIsStalled) exists in this session or any live,
// non-stop-gated descendant.
func (s *Session) subtreeHasLiveComponent() (bool, error) {
	if s.jobManager != nil {
		if s.jobManager.hasRunningDelegate() || s.jobManager.hasPendingWatchSends() {
			return true, nil
		}
	}
	if s.peekNotifications() > 0 {
		return true, nil
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		driving := sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && s.childStopGated(child.id) {
			continue
		}
		if driving {
			return true, nil
		}
		if child != nil {
			live, err := child.subtreeHasLiveComponent()
			if err != nil {
				return false, err
			}
			if live {
				return true, nil
			}
		}
	}
	return false, nil
}

// subtreeOutstandingDelegateIDs collects the outstanding delegate job ids across
// this session and every live, non-stop-gated descendant, so a stall warning can
// name the stuck delegate(s). It walks the same path as treeHasOutstandingWork.
func (s *Session) subtreeOutstandingDelegateIDs() []string {
	var ids []string
	if s.jobManager != nil {
		if got, err := s.jobManager.outstandingDelegateIDs(); err == nil {
			ids = append(ids, got...)
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil || s.childStopGated(child.id) {
			continue
		}
		ids = append(ids, child.subtreeOutstandingDelegateIDs()...)
	}
	return ids
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
	ticker := s.clock.NewTicker(drainRecheckInterval)
	defer ticker.Stop()
	return s.drainJobTree(ctx, ticker.C())
}

// drainJobTree contains the drain loop behind an injectable recheck channel.
// Production always passes the session clock's ticker; deterministic tests can
// explicitly drive rechecks without depending on wall time or advancing every
// unrelated waiter on a shared fake clock.
func (s *Session) drainJobTree(ctx context.Context, recheck <-chan time.Time) (string, error) {
	return s.drainJobTreeWith(ctx, recheck, s.kickDriveTree, s.ProcessInputKind)
}

func (s *Session) drainJobTreeWith(ctx context.Context, recheck <-chan time.Time, kick func(context.Context) error, process func(context.Context, string, []ImageAttachment, EntryKind) (string, error)) (string, error) {
	wake, notify := newDrainWake()
	s.SetNotifyFunc(notify)
	defer s.SetNotifyFunc(nil)

	lastResult := ""
	var stallStart time.Time
	for {
		// Deliver pending watch sends and kick drive-down at EVERY level of the
		// subtree, not just direct children: outstanding work isolated in a
		// grandchild (e.g. a restored or lost-wake watch send whose signal never
		// reached the mid-level rail) must still be driven, or the drain hangs.
		// Caller sends render as a token on the owning session's own rail, so this
		// converts them into queued notifications the loop then drains — a one-shot
		// run must not Close() before that delivery.
		if err := kick(ctx); err != nil {
			return lastResult, err
		}
		if s.peekNotifications() > 0 {
			// A completion is queued on this (root) rail: run a notification turn so
			// the coordinator's model receives it and can dispatch more work or wrap
			// up. The turn's boundary also drives any idle descendant that has
			// undelivered attention. The turn's internal loop drains any further
			// already-pending notifications.
			res, err := process(ctx, "", nil, EntryNotification)
			if err != nil {
				return lastResult, err
			}
			if res != "" {
				lastResult = res
			}
			continue
		}
		outstanding, err := s.treeHasOutstandingWork()
		if err != nil {
			return lastResult, err
		}
		if !outstanding {
			// Nothing pending and nothing outstanding anywhere in the subtree.
			return lastResult, nil
		}
		// Defense-in-depth stall watchdog. Outstanding work with NO live or
		// deliverable component anywhere is a genuine wedge (drainSubtreeIsStalled);
		// track how long that condition holds continuously on the injected clock and
		// give up once it exceeds drainStallTimeout so Close() can proceed. Live work
		// (a running delegate, a driving child, a pending watch send, a queued
		// notification) resets the stall clock, so legitimate long work is never cut.
		stalled, err := s.drainSubtreeIsStalled()
		if err != nil {
			return lastResult, err
		}
		if !stalled {
			stallStart = time.Time{}
		} else {
			now := s.sclock().Now()
			if stallStart.IsZero() {
				stallStart = now
			} else if now.Sub(stallStart) >= drainStallTimeout {
				// The drain is wedged on undelivered work the machinery is not
				// converting. Warn (naming the stuck delegate(s)) and return the last
				// result with nil error so cmd/serf/run.go prints the coordinator's
				// last answer and proceeds to Close(), rather than aborting the run.
				ids := s.subtreeOutstandingDelegateIDs()
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
					"job-tree drain stalled for %s with no live work; giving up so shutdown can proceed (stuck delegates: %s)",
					drainStallTimeout, strings.Join(ids, ", "))})
				return lastResult, nil
			}
		}
		// Work is still in flight in the subtree but this rail has not been
		// signalled yet. Block until a completion wakes us, the periodic re-check
		// fires, or the caller's context is cancelled; the next iteration re-kicks.
		if err := waitDrainWake(ctx, wake, recheck); err != nil {
			return lastResult, err
		}
	}
}

// rematerializeDurablePendings re-enqueues this session's OWN durable terminal
// notifications that are NotifyPending but absent from the in-memory queue, so
// the next notification/drive turn can deliver and settle them.
//
// It closes a gap between the drain's two signals: quiescence is measured on the
// durable ledger (outstandingDelegateCount reads NotifyPending records) while
// delivery is driven off the in-memory queue (the loop's peekNotifications gate,
// and driveChildrenWithUndeliveredAttention only drives a child whose queue is
// non-empty). A pending that survives only in the durable store — a revived
// delegate whose deferred restore side effects were interrupted before
// arm_notifications ran, or a finalize whose in-memory enqueue never landed —
// would otherwise hold the drain open forever: counted as outstanding, but never
// materialized for any turn to deliver.
//
// It re-enqueues exactly the records outstandingDelegateCount holds the drain
// open on (same owned-record filter), so counted == re-materializable and no
// stranded pending is left behind. Like armPendingTerminalNotifications it does
// NOT skip an already-injected record: an owned pending whose <job-notification>
// block is already in history but was never marked Delivered (e.g. a crash
// between appendTurnDurably and markJobNotificationsDelivered, or an interrupted
// deferred restore) still counts as outstanding, and re-enqueuing it settles it —
// the delivery path routes an already-injected record to injectedJobNotifs, which
// marks it Delivered WITHOUT re-appending to history. Unlike arm it appends no
// event, and the empty-queue guard keeps repeated drain kicks idempotent: once a
// re-enqueued notification is delivered the record is Delivered and no longer
// re-materializes.
func (s *Session) rematerializeDurablePendings() error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	// A non-empty queue is already on the delivery path; only a stranded durable
	// pending (nothing queued to deliver it) needs re-materializing.
	if s.peekNotifications() > 0 {
		return nil
	}
	jm := s.jobManager
	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec == nil || rec.Type != jobstore.JobDelegate {
			continue
		}
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		if rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
			continue
		}
		if jm.enqueue != nil {
			jm.enqueue(jobNotificationFromRecord(rec))
		}
	}
	return nil
}

func newDrainWake() (chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	return wake, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func waitDrainWake(ctx context.Context, wake <-chan struct{}, recheck <-chan time.Time) error {
	select {
	case <-wake:
		return nil
	case <-recheck:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kickDriveTree delivers pending watch sends and kicks drive-down at every level
// of the live delegate subtree, skipping stop-gated children (which are never
// driven). drainPendingWatchSends already drives THIS session's direct children
// (its trailing driveChildrenWithUndeliveredAttention pass), and recursing into
// each child repeats that at every level — so outstanding work isolated in a
// grandchild subtree makes progress even when the intervening session's own rail
// carries no immediate signal. All the underlying operations are idempotent and
// self-terminating, so repeated kicks are safe.
func (s *Session) kickDriveTree(ctx context.Context) error {
	return s.kickDriveTreeWith(ctx, func(sess *Session, ctx context.Context) error {
		return sess.drainPendingWatchSends(ctx)
	})
}

func (s *Session) kickDriveTreeWith(ctx context.Context, drain func(*Session, context.Context) error) error {
	if err := s.rematerializeDurablePendings(); err != nil {
		return err
	}
	if err := drain(s, ctx); err != nil {
		return err
	}
	for _, sub := range s.liveDirectSubagents() {
		child := sub.sess
		if child == nil || s.childStopGated(child.id) {
			continue
		}
		if err := child.kickDriveTreeWith(ctx, drain); err != nil {
			return err
		}
	}
	return nil
}
