package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
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

// isOwnedDrainJob reports whether rec is a managed job owned by sessionID. The
// durable record does not reliably preserve a shell's original execution mode,
// so ownership and job type are the complete drain contract.
func isOwnedDrainJob(rec *jobstore.JobRecord, sessionID string) bool {
	if rec == nil {
		return false
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != sessionID {
		return false
	}
	return rec.Type == jobstore.JobShell
}

// outstandingDrainJobCount reports how many managed jobs still owe this session
// a completion notification turn. A managed job counts while it is either still
// in the running map (not yet finalized) or recorded terminal with an owner
// notification that is pending but not yet delivered.
//
// Both halves are needed to be race-free against the finalization sequence
// (jobs.go armFinalizedJob): the running-map membership covers the window from
// EventJobFinished until the durable EventJobNotificationPending is written; the
// NotifyPending record covers the later window after the job is deleted from the
// running map but before its in-memory notification is enqueued. A suppressed
// (watch-origin) notification never reaches NotifyPending, so it correctly stops
// counting once the job leaves the running map — the drain must not wait on a
// notification that will never arrive.
//
// The durable snapshot and the running-map read are taken under the SAME jm.mu
// hold. armFinalizedJob deletes from the running map under jm.mu and appends
// EventJobNotificationPending before that delete, so while this holds jm.mu a
// finalizing managed job is either still in the running map (delete blocked) or its
// NotifyPending record is already durable (delete done, its earlier append
// visible to Load). Loading the store outside the lock would reopen the
// stale-snapshot window. Store never acquires jm.mu, so jm.mu -> store.mu is a
// consistent order with no inverse. A store read error is returned, never
// folded into a zero count: an unreadable store is not quiescence.
func (jm *jobManager) outstandingDrainJobCount() (int, error) {
	ids, err := jm.outstandingDrainJobIDs()
	return len(ids), err
}

// outstandingDrainJobIDs lists the job ids outstandingDrainJobCount counts: this
// session's own managed jobs still in the running map, plus its own managed jobs
// recorded terminal with a NotifyPending owner notification. The stall watchdog
// uses the ids to name the stuck managed job(s) in its warning; the count is just
// len() of this list, so the two can never disagree.
func (jm *jobManager) outstandingDrainJobIDs() ([]string, error) {
	ids, _, err := jm.outstandingDrainJobIDsByBackground()
	return ids, err
}

// outstandingDrainJobIDsByBackground returns outstandingDrainJobIDs together
// with the subset of them that are LIVE background shells, under a single jm.mu
// hold so the two lists can never disagree.
//
// background is read off the RUNNING MAP's own record. JobRecord.Background is
// json:"-" — no event carries it, it is stamped in memory at launch or
// promotion and nowhere else — so a record folded from the store always reads
// false whatever the job did. That is what isOwnedDrainJob's "does not
// reliably preserve a shell's original execution mode" comment refers to, and
// it is why the durable NotifyPending half below contributes to `all` only: a
// terminal job still owing a notification is deliverable work the drain
// resolves on its own, and calling it background would let the undisposed-job
// announcement fire while real progress was one turn away.
func (jm *jobManager) outstandingDrainJobIDsByBackground() (all, background []string, err error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	recs, err := jm.store.Load()
	if err != nil {
		return nil, nil, err
	}
	counted := make(map[string]bool)
	var ids []string
	for id, run := range jm.running {
		if isOwnedDrainJob(run.rec, jm.sessionID) {
			counted[id] = true
			ids = append(ids, id)
			if run.rec.Background && run.stopStatus == "" {
				background = append(background, id)
			}
		}
	}
	for id, rec := range recs {
		if counted[id] {
			continue
		}
		// Only this session's OWN managed-job notifications hold the drain open. A
		// forwarded descendant copy (OwnerSessionID names a child, not this
		// session) is a drive signal for that child's attention; the child is
		// covered by the recursive tree walk — and skipped there when stop-gated.
		// Counting the forwarded copy here would hang the drain on a stop-gated
		// child that nothing will ever settle (matches the owned-records filter in
		// armPendingTerminalNotifications).
		if isOwnedDrainJob(rec, jm.sessionID) && rec.NotifyState == jobstore.NotifyPending {
			ids = append(ids, id)
		}
	}
	return ids, background, nil
}

// hasRunningDrainJob reports whether any managed job is still in the running
// map — a managed job that has not finalized is LIVE work (a long build, a slow
// tool), never a stall. The drain stall watchdog treats a running managed job
// anywhere in the subtree as a reason to keep waiting.
func (jm *jobManager) hasRunningDrainJob() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, run := range jm.running {
		if isOwnedDrainJob(run.rec, jm.sessionID) {
			return true
		}
	}
	return false
}

// treeHasOutstandingWork reports whether this session or any live descendant in
// its managed-job subtree still owes drain work: an outstanding managed job, a
// pending job notification, a pending caller-targeted watch send, a pending
// delegate delivery, an in-flight stable delegate run, or an in-flight drive
// turn. Close() cancels the whole subtree, so the one-shot drain must settle
// all of it — a root whose direct managed job finished after spawning its own
// fire-and-return managed job or stable delegate is not quiescent until that
// descendant's work drains too.
//
// hasPendingDelegateDeliveries matters here for a straddle PRI-2441's own fix
// does not cover: acceptDelegateDeliveryPlan defers a delivery into that queue
// (instead of delivering it inline) whenever the receiving session is
// SessionProcessing at that instant — e.g. a second, unrelated delegate's
// completion targeting this session while the drain's own notification turn is
// already running one delivery. The defer calls notify() exactly once; nothing
// else about the session's state changes. Without this check, that one wake can
// be consumed by a later pass with the delivery still sitting unflushed, and
// the drain declares quiescence with a real completion undelivered. kickDriveTree
// (below) is what actually resolves a pending delivery once this check keeps the
// drain from quiescing prematurely — the two halves both matter, see kickDriveTreeWith.
//
// A store read error is propagated (not folded into quiescence): the drain must
// neither Close() on an unreadable store nor spin on it forever, so the loop
// surfaces the failure instead.
func (s *Session) treeHasOutstandingWork() (bool, error) {
	if s.jobManager != nil {
		n, err := s.jobManager.outstandingDrainJobCount()
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return s.treeHasOutstandingWorkBesidesOwnJobs()
}

// treeHasOutstandingWorkBesidesOwnJobs is treeHasOutstandingWork with this
// session's OWN outstanding managed jobs left out — every other signal, at every
// level, including a descendant's managed jobs.
//
// The undisposed-background-job announcement needs exactly this question. It
// fires only when the drain's sole remaining reason to wait is a background
// shell of this session's, and it measures that on this session's own
// outstanding set; but the drain blocks on the whole SUBTREE. A live child
// keeps treeHasOutstandingWork true while this session's own set is just the
// background shell, so without this split the announcement would name a false
// reason to the model.
func (s *Session) treeHasOutstandingWorkBesidesOwnJobs() (bool, error) {
	if s.jobManager != nil && s.jobManager.hasPendingWatchSends() {
		return true, nil
	}
	if s.peekNotifications() > 0 {
		return true, nil
	}
	if s.hasPendingDelegateDeliveries() {
		return true, nil
	}
	if s.hasPendingRootDelegateAttention() {
		return true, nil
	}
	if s.hasPendingDelegateAttentionArmRetry() {
		return true, nil
	}
	if s.hasPendingStableDelegateAttention() {
		return true, nil
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		active := sub.running || sub.finalizing || sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id)) {
			// A deliberately stopped or fatally failed child is never driven —
			// driveChildrenWithUndeliveredAttention skips both gates — so its
			// attention will never be
			// delivered. Counting it (or its subtree) would hang the drain forever,
			// so match the drive gate and skip it.
			continue
		}
		if active {
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

// undisposedBackgroundDrainJobs reports the background shells that are the
// ONLY remaining reason this drain is still waiting AND that the model has not
// marked awaited with a watch — sorted, so callers can key escalation state on
// the set — and sole=false whenever anything else is outstanding.
//
// The conditions are each load-bearing. Restricting it to BACKGROUND shells
// separates a service the model started from a foreground command still
// finishing, which the drain resolves on its own. Requiring that ALL of this
// session's outstanding jobs are background keeps a live background job from
// suppressing a drain that legitimately owes a completion turn elsewhere.
// Requiring that nothing else in the subtree is outstanding keeps the claim
// honest: a live child is a real reason to wait, and telling the model
// otherwise would announce a false alarm. And an ARMED WATCH excuses a job
// entirely — it is the model's explicit "this terminates and I need its
// result", the one answer that means the drain should wait exactly as it
// always has.
func (s *Session) undisposedBackgroundDrainJobs() ([]string, bool, error) {
	undisposed, _, sole, _, err := s.backgroundDrainState()
	return undisposed, sole, err
}

// backgroundDrainState returns the current undisposed candidates, the sorted
// background set they came from, whether those candidates are the sole reason
// to wait, and whether every job in that set has a live watch. The last value
// lets the drain distinguish a watch-excused episode from an ordinary
// non-candidate pass, so clearing a watch can reset escalation state.
func (s *Session) backgroundDrainState() (undisposed, background []string, sole, allWatched bool, err error) {
	if s.jobManager == nil {
		return nil, nil, false, false, nil
	}
	var all []string
	all, background, err = s.jobManager.outstandingDrainJobIDsByBackground()
	if err != nil {
		return nil, nil, false, false, err
	}
	if len(background) == 0 || len(background) != len(all) {
		return nil, background, false, false, nil
	}
	sort.Strings(background)
	allWatched = true
	for _, id := range background {
		if !s.jobManager.hasLiveWatchOnTarget(id) {
			allWatched = false
			break
		}
	}
	elsewhere, err := s.treeHasOutstandingWorkBesidesOwnJobs()
	if err != nil {
		return nil, background, false, allWatched, err
	}
	if elsewhere {
		return nil, background, false, allWatched, nil
	}
	undisposed = background[:0]
	for _, id := range background {
		if !s.jobManager.hasLiveWatchOnTarget(id) {
			undisposed = append(undisposed, id)
		}
	}
	if len(undisposed) == 0 {
		return nil, background, false, allWatched, nil
	}
	sort.Strings(undisposed)
	return undisposed, background, true, allWatched, nil
}

func (jm *jobManager) watchHistoryIDs() map[string]struct{} {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	seen := make(map[string]struct{}, len(jm.watchHistory))
	for _, entry := range jm.watchHistory {
		seen[entry.id] = struct{}{}
	}
	return seen
}

func (jm *jobManager) hasNewWatchEndOnTargets(targets []string, seen map[string]struct{}) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	targetSet := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetSet[target] = struct{}{}
	}
	newEnd := false
	for _, entry := range jm.watchHistory {
		if _, known := seen[entry.id]; known {
			continue
		}
		seen[entry.id] = struct{}{}
		if _, matches := targetSet[entry.target]; matches {
			newEnd = true
		}
	}
	return newEnd
}

// undisposedBackgroundJobsAnnouncement is the first escalation turn: the run
// cannot finish while these jobs are outstanding, and the model — the only
// party who knows what each job is — must pick one of three dispositions.
// Never a duration question: models are bad at estimating how long their own
// jobs take (the withdrawn max_runtime_ms taught that), and each remedy here
// is a categorical act. job_stop is deliberately framed as the SCRATCH answer,
// never the default: the destructive action must not be the cheap path.
func undisposedBackgroundJobsAnnouncement(jobIDs []string, shellTool string, canDetach bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This run cannot finish with undisposed background job(s): %s. The process exits once this turn's work is drained; a job still running then is killed and its output lost. Decide what each job is:\n",
		strings.Join(jobIDs, ", "))
	b.WriteString("- Scratch work you no longer need: stop it with job_stop.\n")
	if canDetach {
		fmt.Fprintf(&b, "- A process that must outlive this run (a server, a watcher): stop it with job_stop FIRST, then relaunch the same command with %s mode=\"detached\". Stop first — relaunching without stopping leaves the original running, and anything bound to a port will fail to rebind. A detached process's output is discarded (redirect to a file if you need it) and it sends no completion notification.\n", shellTool)
	} else {
		b.WriteString("- A process that must outlive this run: this environment cannot disown a process, so nothing can outlive it. Stop the job and say so plainly in your final answer.\n")
	}
	b.WriteString("- A command that terminates on its own whose result you need: create job_watch(operation=\"create\", source=\"<job_id>\", progress_interval_ms=120000) and the run will wait for it.\n")
	b.WriteString("If you do nothing, this run will ask once more and then exit, killing the job(s).")
	return b.String()
}

// undisposedBackgroundJobsFinalWarning is the second and last escalation turn.
func undisposedBackgroundJobsFinalWarning(jobIDs []string, canDetach bool) string {
	remedy := "Stop them (job_stop), or mark them awaited (job_watch) and report what happened in THIS turn"
	if canDetach {
		remedy = "Stop them (job_stop), then detach them, or mark them awaited (job_watch) in THIS turn"
	}
	return fmt.Sprintf("Final notice: background job(s) %s are still undisposed. %s; otherwise this run exits now and they are killed, their output lost.",
		strings.Join(jobIDs, ", "), remedy)
}

// detachedShellAvailable reports whether this session could actually relaunch
// a command with mode:"detached". An environment that does not report the
// capability is treated as unable: recommending a call that returns
// ErrDetachUnsupported is worse than not offering it.
func (s *Session) detachedShellAvailable() bool {
	reporter, ok := s.currentEnv().(execenv.DetachSupportReporter)
	return ok && reporter.DetachSupported()
}

// describeUndisposedJobs renders the killed-jobs report: id, command text,
// runtime, and quiet time per job, off the live record. A bare job id is not
// diagnosable after the process has exited; the command is what tells an
// operator what actually died.
func (jm *jobManager) describeUndisposedJobs(ids []string) string {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	now := jm.clock.Now()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		run := jm.running[id]
		if run == nil || run.rec == nil {
			parts = append(parts, id)
			continue
		}
		quiet := now.Sub(run.rec.StartedAt)
		if run.rec.LastActivity != nil {
			quiet = now.Sub(*run.rec.LastActivity)
		}
		parts = append(parts, fmt.Sprintf("%s (%q, ran %s, quiet %s)",
			id, run.rec.Command, now.Sub(run.rec.StartedAt).Round(time.Second), quiet.Round(time.Second)))
	}
	return strings.Join(parts, ", ")
}

// drainSubtreeIsStalled reports whether the subtree is GENUINELY WEDGED: it
// still owes drain work (treeHasOutstandingWork) yet contains NO live or
// deliverable component anywhere. This is the only condition under which the
// stall watchdog is allowed to give up, and it is deliberately narrow so it can
// never cut legitimate work.
//
// The six live/deliverable components — any of them anywhere in the subtree
// means NOT stalled — are:
//   - a managed job still in some jm.running (hasRunningDrainJob): live work such
//     as a long build produces no drain progress for minutes yet is not wedged;
//   - a running or finalizing stable delegate: live work that no longer has a
//     delegate JobRecord after the stable-resource cutover;
//   - a driving child (sub.driving): a drive turn is in flight;
//   - a pending caller-targeted watch send (hasPendingWatchSends): deliverable;
//   - a queued notification at any level (peekNotifications > 0): deliverable now;
//   - a pending delegate delivery (hasPendingDelegateDeliveries): deliverable —
//     kickDriveTree flushes it every pass, so it never sits long enough to wedge.
//
// When outstanding is true but none of those exist, the outstanding work is
// composed entirely of terminal-but-undelivered notifications that the
// drive/recheck machinery is not converting — a stranded wedge. It walks the
// same liveDirectSubagents / child gate path treeHasOutstandingWork does,
// skipping stop-gated and fatally failed children identically (they are never
// driven, so their leftover state is not a stall the drain can act on).
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
// non-gated descendant.
func (s *Session) subtreeHasLiveComponent() (bool, error) {
	if s.jobManager != nil {
		if s.jobManager.hasRunningDrainJob() || s.jobManager.hasPendingWatchSends() {
			return true, nil
		}
	}
	if s.peekNotifications() > 0 {
		return true, nil
	}
	if s.hasPendingDelegateDeliveries() {
		return true, nil
	}
	if s.hasPendingRootDelegateAttention() {
		return true, nil
	}
	if s.hasPendingDelegateAttentionArmRetry() {
		return true, nil
	}
	if s.hasPendingStableDelegateAttention() {
		return true, nil
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		active := sub.running || sub.finalizing || sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id)) {
			continue
		}
		if active {
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

// subtreeOutstandingDrainJobIDs collects the outstanding managed job ids across
// this session and every live, non-gated descendant, so a stall warning can
// name the stuck managed job(s). It walks the same path as treeHasOutstandingWork.
func (s *Session) subtreeOutstandingDrainJobIDs() []string {
	var ids []string
	if s.jobManager != nil {
		if got, err := s.jobManager.outstandingDrainJobIDs(); err == nil {
			ids = append(ids, got...)
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil || s.childStopGated(child.id) || s.childFatalRunGated(child.id) {
			continue
		}
		ids = append(ids, child.subtreeOutstandingDrainJobIDs()...)
	}
	return ids
}

// DrainJobTree keeps re-driving the coordinator on managed-job completions until no
// managed job anywhere in the subtree still owes a notification turn, then returns
// the last notification turn's result (empty when no drain turn ran). It is the
// one-shot analogue of the serve loop's notification pump: a coordinator that
// fires a fire-and-return managed job (max_wait_ms=0) ends its turn while the child
// is still running, so without this drain the caller's Close() would SIGKILL the
// child (and any descendant) before it finishes (PRI-2441).
//
// The wait is bounded by ctx: a subtree whose managed work never completes
// blocks until the caller's context is cancelled. Individual managed-job turns
// carry their own round/time caps, so a well-formed tree always quiesces on its
// own. Every owned managed job holds the drain open.
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
	// bgAnnounced counts undisposed-background-job announcements per job SET
	// (sorted ids joined). Keying on the set, not a bool, is what lets a job
	// started during an announcement turn get its own full escalation instead of
	// inheriting a spent count — and what stops a stop-and-relaunch from being
	// nagged as if it had already been warned.
	//
	// bgArmed is the set observed by the previous pass that went on to block in
	// waitDrainWake: the FIRST announcement fires only when the condition
	// survives one park. A job that is finishing right now — the ordinary
	// launch-background-and-end-turn handoff — finalizes during that park and
	// its completion wake delivers a notification turn instead, so it is never
	// announced; a job that is genuinely not finishing meets the next recheck
	// tick still running. No new constant: the park is the drain's existing
	// wake/recheck cadence.
	bgAnnounced := make(map[string]int)
	bgArmed := ""
	watchSuppressed := ""
	watchHistorySeen := s.jobManager.watchHistoryIDs()
	for {
		// Take this pass's wake edge before it reads any state. treeHasOutstandingWork
		// consults eight independent signals in sequence, so it is not a snapshot, and
		// a completion that hands work UP the tree moves two of them in turn:
		// deliverDelegatePacket arms the coordinator's root attention and only THEN
		// does runSubagent clear the delegating subagent's finalizing flag. The pass
		// reads the attention early and the flag late, so a pass that straddles the
		// hand-off sees both false though at no instant were they both false — and
		// returns "" while an armed delegate notification waits, leaving Close() to
		// SIGKILL it (the PRI-2441 B1 flake).
		//
		// Every producer of drain-relevant work raises this wake, so it is the one
		// signal that survives the straddle. Consuming the edge here and re-checking
		// it below turns "I looked and saw nothing" into "I looked and saw nothing,
		// and nothing moved while I looked" — the only claim that justifies letting
		// Close() cancel the subtree.
		takeDrainWake(wake)
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
		if s.peekNotifications() > 0 || s.hasPendingRootDelegateAttention() {
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
			// A delivered notification is real drain progress: reset the stall
			// clock so the watchdog measures only continuous stall, never a stall
			// episode punctuated by deliveries.
			stallStart = time.Time{}
			continue
		}
		outstanding, err := s.treeHasOutstandingWork()
		if err != nil {
			return lastResult, err
		}
		if !outstanding {
			// Nothing pending and nothing outstanding anywhere in the subtree — but the
			// scan above is not a snapshot. A wake raised since this pass took its edge
			// means the tree moved while the scan ran, so re-run the pass instead of
			// trusting a verdict assembled across the change. This terminates: a wake
			// is only raised by a real state change, and a quiescent subtree raises
			// none, so the confirming pass finds the edge clear and returns.
			if takeDrainWake(wake) {
				continue
			}
			return lastResult, nil
		}
		// A one-shot run cannot finish with an undisposed background job: the
		// process exits when this drain returns, so a job still running dies
		// with it, unreported. When such jobs are the drain's SOLE remaining
		// reason to wait, tell the model — the only party who knows whether
		// each job is scratch (stop it), a service (detach it), or bounded
		// work whose result the answer needs (watch it). The escalation is
		// paced by the model's own turns, never a clock: each announcement IS
		// a turn, and the count advances only when a turn completes with the
		// set still undisposed. Announce; announce again naming the
		// consequence; then stop waiting so Close()'s kill path can run.
		//
		// The announcement turns are housekeeping, so their replies never fold
		// into lastResult (a run's printed answer must not become "I stopped
		// job_2"), and their errors never fail the drain — a provider error on
		// a housekeeping turn must not convert a successful run into a failed
		// one. A failed turn still advances the escalation: the alternative is
		// retrying a broken provider forever with the process held open.
		//
		// Under serve the session outlives the turn, background jobs genuinely
		// report later, and none of this fires (TurnEndsProcess is the gate).
		if s.cfg.TurnEndsProcess {
			undisposed, background, sole, allWatched, err := s.backgroundDrainState()
			if err != nil {
				return lastResult, err
			}
			backgroundSetKey := strings.Join(background, ",")
			watchCleared := len(background) > 0 && s.jobManager.hasNewWatchEndOnTargets(background, watchHistorySeen)
			if watchCleared {
				if watchSuppressed != "" {
					delete(bgAnnounced, watchSuppressed)
					if bgArmed == watchSuppressed {
						bgArmed = ""
					}
				} else {
					delete(bgAnnounced, backgroundSetKey)
					if bgArmed == backgroundSetKey {
						bgArmed = ""
					}
				}
				watchSuppressed = ""
			}
			// A live watch excuses a background set for an entire escalation
			// episode. Once any watch in that set is cleared, forget the old
			// escalation so the next episode starts with the first warning.
			if watchSuppressed != "" && (backgroundSetKey != watchSuppressed || !allWatched) {
				delete(bgAnnounced, watchSuppressed)
				if bgArmed == watchSuppressed {
					bgArmed = ""
				}
				watchSuppressed = ""
			}
			if allWatched {
				watchSuppressed = backgroundSetKey
			}
			if !sole {
				bgArmed = ""
			} else if setKey := strings.Join(undisposed, ","); bgArmed != setKey && bgAnnounced[setKey] == 0 {
				// First sighting of this set: arm, and let the pass fall through
				// to waitDrainWake. A completion in flight beats the recheck tick
				// and is delivered instead of announced.
				bgArmed = setKey
			} else {
				switch bgAnnounced[setKey] {
				case 0, 1:
					canDetach := s.detachedShellAvailable()
					text := undisposedBackgroundJobsAnnouncement(
						undisposed, s.providerVisibleToolName("shell"), canDetach)
					if bgAnnounced[setKey] == 1 {
						text = undisposedBackgroundJobsFinalWarning(undisposed, canDetach)
					}
					bgAnnounced[setKey]++
					// EntryNotification, not EntrySteeringCarrier: Steer enqueues
					// DAEMON-sourced steering, and the carrier's entry gate counts
					// only user-sourced steering, so a carrier turn stands down
					// before any model call — which is exactly how an earlier
					// attempt at this shipped inert.
					s.SteerKind(text, events.SteeringKindNotification)
					if _, perr := process(ctx, "", nil, EntryNotification); perr != nil {
						s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
							"undisposed-background-job announcement turn failed: %v", perr)})
					}
					stallStart = time.Time{}
					continue
				default:
					// Told twice, declined twice. Same wake-edge protocol as every
					// other return that lets Close() cancel the subtree: a wake
					// raised mid-scan means the tree moved, so re-run the pass
					// rather than killing work that armed while this pass looked.
					if takeDrainWake(wake) {
						continue
					}
					s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
						"exiting with %d undisposed background job(s) after two declined announcements; they die with the process: %s",
						len(undisposed), s.jobManager.describeUndisposedJobs(undisposed))})
					return lastResult, nil
				}
			}
		}
		// Defense-in-depth stall watchdog. Outstanding work with NO live or
		// deliverable component anywhere is a genuine wedge (drainSubtreeIsStalled);
		// track how long that condition holds continuously on the injected clock and
		// give up once it exceeds drainStallTimeout so Close() can proceed. Live work
		// (a running managed job, a driving child, a pending watch send, a queued
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
				// The stall verdict was assembled across sequential reads, not a
				// snapshot. A wake raised since this pass took its edge means the
				// tree moved mid-verdict — the same straddle the quiescence return
				// guards against — so re-run the pass rather than letting Close()
				// SIGKILL work that armed while the scan ran. A genuinely wedged
				// tree raises no wake, so the confirming pass gives up cleanly.
				if takeDrainWake(wake) {
					continue
				}
				// The drain is wedged on undelivered work the machinery is not
				// converting. Warn (naming the stuck managed job(s)) and return the last
				// result with nil error so cmd/evener/run.go prints the coordinator's
				// last answer and proceeds to Close(), rather than aborting the run.
				ids := s.subtreeOutstandingDrainJobIDs()
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
					"job-tree drain stalled for %s with no live work; giving up so shutdown can proceed (stuck managed jobs: %s)",
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
// durable ledger (outstandingDrainJobCount reads NotifyPending records) while
// delivery is driven off the in-memory queue (the loop's peekNotifications gate,
// and driveChildrenWithUndeliveredAttention only drives a child whose queue is
// non-empty). A pending that survives only in the durable store — a revived
// managed job whose deferred restore side effects were interrupted before
// arm_notifications ran, or a finalize whose in-memory enqueue never landed —
// would otherwise hold the drain open forever: counted as outstanding, but never
// materialized for any turn to deliver.
//
// It re-enqueues exactly the records outstandingDrainJobCount holds the drain
// open on (same owned-record filter), so counted == re-materializable and no
// stranded pending is left behind. Like armPendingTerminalNotifications it does
// NOT skip an already-injected record: an owned pending whose <job-notification>
// block is already in history but was never marked Delivered (e.g. a crash
// between appendSteeringTurnDurably and markJobNotificationsDelivered, or an interrupted
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
	// A job still in the running map is mid-finalization in armFinalizedJob:
	// its NotifyPending record is the transient state between the
	// EventJobNotificationPending append and the NotifyPending→NotifyDelivered
	// transition (persistStableShellAttention) or the plain owner enqueue, both
	// of which run before the job is deleted from the running map. Re-enqueuing
	// such a record during that window double-delivers (issue #140):
	// armFinalizedJob still owns its notification. rematerialize is for pendings
	// that survive ONLY in the durable store after the run is gone, so skip any
	// record whose run is still live.
	//
	// The durable load and the running-map snapshot are taken under the SAME
	// jm.mu hold, for the reason documented on outstandingDrainJobIDs: a
	// finalizing job is either still in the running map (its delete under jm.mu
	// is blocked) or its NotifyDelivered append — which precedes that delete —
	// is already visible to Load. Loading outside the lock would let a
	// finalization complete between the load and the snapshot, and the stale
	// NotifyPending record would be re-enqueued after its delivery — the same
	// extra finalization turn through a narrower window. jm.mu -> store.mu is
	// the established order; the store never acquires jm.mu.
	jm.mu.Lock()
	recs, err := jm.store.Load()
	if err != nil {
		jm.mu.Unlock()
		return err
	}
	liveRunning := make(map[string]struct{}, len(jm.running))
	for id := range jm.running {
		liveRunning[id] = struct{}{}
	}
	hook := jm.testOnlyAfterRematerializeDurableLoad
	jm.mu.Unlock()
	if hook != nil {
		hook()
	}
	for _, rec := range recs {
		if !isOwnedDrainJob(rec, jm.sessionID) {
			continue
		}
		if rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
			continue
		}
		if _, live := liveRunning[rec.JobID]; live {
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

// takeDrainWake consumes a pending wake edge without blocking and reports
// whether one was there. The wake channel holds a single coalesced edge, so this
// is "has anything signalled since I last asked", never a count.
func takeDrainWake(wake <-chan struct{}) bool {
	select {
	case <-wake:
		return true
	default:
		return false
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

// kickDriveTree delivers pending watch sends, flushes pending delegate
// deliveries, and kicks drive-down at every level of the live managed-job
// subtree, skipping stop-gated children (which are never driven).
// drainPendingWatchSends already drives THIS session's direct children (its
// trailing driveChildrenWithUndeliveredAttention pass), and recursing into
// each child repeats that at every level — so outstanding work isolated in a
// grandchild subtree makes progress even when the intervening session's own rail
// carries no immediate signal. All the underlying operations are idempotent and
// self-terminating, so repeated kicks are safe.
//
// Flushing delegate deliveries here matters specifically for the drain: a
// delivery deferred by acceptDelegateDeliveryPlan (because the receiving
// session was SessionProcessing at that instant) sits in pendingDelegateDeliveries
// until something calls flushPendingDelegateDeliveries, and outside a live turn
// nothing else will — the drain loop's own notification turns only run when
// treeHasOutstandingWork already sees something to report, which a merely
// PENDING delivery does not become until it is flushed. Without this call the
// wake treeHasOutstandingWork now waits on (see its doc) would never resolve:
// the drain would sit correctly refusing to quiesce, but nothing would ever
// convert the pending delivery into the notification/attention state that lets
// it quiesce for real. Calling it here, every pass, before the outstanding
// check, closes that gap the same way rematerializeDurablePendings closes the
// durable/in-memory job-notification gap just below.
//
// flushPendingDelegateDeliveries carries no s.state check of its own — its four
// other call sites are all safe only because each runs ON the owning session's
// own turn goroutine. This recursion instead calls it from the ANCESTOR's drain
// goroutine, so a child that is actively running/driving/finalizing must be
// skipped (see the busy check in the loop below), matching the gate
// driveSubagentNotificationTurn already applies before touching a child's own
// turn machinery: flushing into a busy child's history at a point its own round
// loop did not choose can splice a delivery between an in-flight tool call and
// its result. Only the flush is skipped, not the rest of the kick (watch-send
// delivery and recursion into grandchildren still run) — the child's own
// pending count still holds the ancestor's drain open via treeHasOutstandingWork
// (which reads it recursively too), and the child's own turn flushes it at its
// next natural boundary once it stops being busy.
func (s *Session) kickDriveTree(ctx context.Context) error {
	return s.kickDriveTreeWith(ctx, func(sess *Session, ctx context.Context) error {
		return sess.drainPendingWatchSends(ctx)
	})
}

func (s *Session) kickDriveTreeWith(ctx context.Context, drain func(*Session, context.Context) error) error {
	return s.kickDriveTreeWithFlush(ctx, drain, true)
}

// kickDriveTreeWithFlush is kickDriveTreeWith's recursive body. flushDeliveries
// gates only this session's own flushPendingDelegateDeliveries call — see
// kickDriveTree's doc for why a busy child must not have it called on its
// behalf from the ancestor's goroutine. The root/direct caller always passes
// true: DrainJobTree only ever starts once the caller's own turn has already
// ended (PRI-2441's premise), so the top-level session is never mid-turn here.
func (s *Session) kickDriveTreeWithFlush(ctx context.Context, drain func(*Session, context.Context) error, flushDeliveries bool) error {
	s.drivePendingStableDelegateAttention()
	if flushDeliveries {
		if err := s.flushPendingDelegateDeliveries(); err != nil {
			return err
		}
	}
	if err := s.rematerializeDurablePendings(); err != nil {
		return err
	}
	if err := drain(s, ctx); err != nil {
		return err
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		childBusy := sub.running || sub.driving || sub.finalizing
		child := sub.sess
		sub.mu.Unlock()
		if child == nil || s.childStopGated(child.id) || s.childFatalRunGated(child.id) {
			continue
		}
		if err := child.kickDriveTreeWithFlush(ctx, drain, !childBusy); err != nil {
			return err
		}
	}
	return nil
}
