package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

type drainAbandonedChild struct {
	delegateID string
	generation uint64
}

type drainGraceChild struct {
	delegateID                 string
	generation                 uint64
	latestProductiveActivityAt time.Time
	secondWindowStartedAt      time.Time
}

// DrainStallTimeout bounds how long the one-shot drain will keep blocking on a
// subtree that is outstanding but has NO live or deliverable component anywhere
// (a genuine stall — see drainSubtreeIsStalled). It is a defense-in-depth
// backstop for an unknown future stranding class: in a correct build the drain
// self-heals (rematerializeDurablePendings) and this predicate is never true for
// long, so the timeout only bites a regression. It is deliberately GENEROUS —
// a stalled tree owes no live work, so waiting two minutes before giving up
// costs nothing but guarantees an otherwise-forever hang cannot survive it.
// A package var (not a const) so tests override and restore it without wall
// time, matching the close-cascade budget in jobs.go. It is EXPORTED because the
// drain's only end-to-end shape is a whole one-shot `evener run`, which lives
// in another module (cmd/evener) and so cannot reach an unexported var — the
// same reason SessionConfig.LLMRetryPolicy is exported. Nothing in production
// assigns it; the drain is a process-wide policy, so a process-wide var is its
// home and no per-session plumbing is needed for descendants.
var DrainStallTimeout = 2 * time.Minute

// backgroundDispositionDelay gives an undisposed background shell one completion
// handoff before the model is asked to stop, detach, or watch it. It is not a
// notification polling cadence.
const backgroundDispositionDelay = 250 * time.Millisecond

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
			// A job whose terminal record exists is finalizing (armFinalizedJob
			// is between its EventJobFinished and its running-map delete): one
			// wake from delivery, never an undisposed job to announce.
			if run.rec.Background && run.stopStatus == "" && run.terminal == nil {
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

// ManagedJobsFinalizedForTest reports whether none of this session's own
// managed jobs is still inside finalization: the running map is clear of them,
// which — because armFinalizedJob (jobs.go) appends the durable
// EventJobNotificationPending BEFORE it deletes the running entry — means every
// completion this drain owes the model is already recorded as a pending owner
// notification.
//
// Test-only seam. cmd/evener's drain tests hold the drain until the shell they
// launched has left the running map, so their scripted provider steps start
// from the state a real run reaches after the completion wake: the owner
// notification durable and on its way to the queue. A drain that starts
// earlier does not announce a finished shell — a job still in the running map
// with its terminal record written is excluded from the background set
// (outstandingDrainJobIDsByBackground), and after the delete the NotifyPending
// record is deliverable work both the sole-reason test and
// treeHasOutstandingWork count. What the barrier buys the scripts is
// a fixed step order, the completion as the next turn, instead of a race
// between the drain's first pass and an executor exit the manager cannot see
// until Wait returns.
func (s *Session) ManagedJobsFinalizedForTest() bool {
	if s == nil || s.jobManager == nil {
		return false
	}
	return !s.jobManager.hasRunningDrainJob()
}

// treeHasOutstandingWork reports whether this session or any live descendant in
// its managed-job subtree still owes drain work: an outstanding managed job, a
// pending job notification, a pending caller-targeted watch send, a pending
// delegate delivery, an in-flight stable delegate run, or an in-flight drive
// turn. Close() cancels the whole subtree, so the one-shot drain must settle
// all of it — a root whose direct managed job finished after spawning its own
// fire-and-return managed job or stable delegate is not quiescent until that
// descendant's work drains too.
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
	children := s.liveDirectSubagents()
	if s.hasPendingDrainDelegateDeliveries(false, children) {
		return true, nil
	}
	// A Stop's park defers pending attention to re-engagement, so it must
	// not hold a drain open either.
	if s.isRootDelegateAttentionReceiver() && s.notificationWorkPending() {
		return true, nil
	}
	for _, sub := range children {
		active, child := s.subagentActive(sub)
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id)) {
			// A deliberately stopped or failed subtree is not runnable. Its
			// durable notifications remain subject to the lifecycle fence and
			// must not keep resource quiescence open.
			//
			// The abandoned gate belongs here as much as in
			// subtreeHasLiveComponent, and #382 item 3 is why: the
			// undisposed-background-job announcement fires only when those jobs
			// are the SOLE reason to wait, and it measures "sole" through this
			// function. With the gate only on the liveness side, one running
			// background shell kept subtreeHasLiveComponent true (so the stall
			// watchdog never armed) while one wedged delegate kept this true (so
			// the announce ladder never armed): each escape suppressed by the
			// other family's work, both dead, forever.
			continue
		}
		if child != nil && s.childDrainGracePending(child.id) {
			// Window two is still owed, so this remains outstanding work. Unlike an
			// abandoned child it must keep the undisposed-shell ladder from exiting
			// early in a mixed shell/delegate tree.
			return true, nil
		}
		if active {
			return true, nil
		}
		// A delegate whose finish append failed reads quiet on its own run flags
		// but still holds an open generation behind the controller's recovery
		// latch. Report it outstanding-but-not-live: the drain waits, and the
		// stall watchdog gives up after DrainStallTimeout instead of quiescing on
		// an unsettled generation (finalizationRecoveryHolds; issue #179 item b).
		if s.childDelegateRecoveryHeld(child) {
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

// childDelegateRecoveryHeld reports whether child names a stable delegate whose
// last generation failed its finish append and is still held behind the
// controller's recovery latch (see finalizationRecoveryHolds).
func (s *Session) childDelegateRecoveryHeld(child *Session) bool {
	return child != nil && child.owningDelegateID != "" && s.delegateController.finalizationRecoveryHolds(child.owningDelegateID)
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
	undisposed = make([]string, 0, len(background))
	allWatched = true
	for _, id := range background {
		if !s.jobManager.hasLiveUnfiredWatchOnTarget(id) {
			allWatched = false
			undisposed = append(undisposed, id)
		}
	}
	elsewhere, err := s.treeHasOutstandingWorkBesidesOwnJobs()
	if err != nil {
		return nil, background, false, allWatched, err
	}
	if elsewhere {
		return nil, background, false, allWatched, nil
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
		fmt.Fprintf(&b, "- A process that must outlive this run (a server, a watcher): stop it with job_stop FIRST, then relaunch the same command with %s mode=\"detached\". Stop first — relaunching without stopping leaves the original running, and anything bound to a port will fail to rebind. A detached process's output is discarded and it sends no completion notification, and it gets no session scratch ($EVENER_SCRATCH_DIR is unset there), so give its command absolute paths for its output and for any file it reads.\n", shellTool)
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
	return detachSupportedBy(s.currentEnv())
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
//   - a pending source delivery: the paced transport retry can transfer custody.
//
// When outstanding is true but none of those exist, the outstanding work is
// composed entirely of terminal-but-undelivered notifications that the
// drive/recheck machinery is not converting — a stranded wedge. It walks the
// same liveDirectSubagents / child gate path treeHasOutstandingWork does,
// skipping stop-gated and fatally failed children identically (they are never
// driven, so their leftover state is not a stall the drain can act on) — plus
// a child this drain has given up on (childDrainAbandoned), which is not live
// work either.
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

// subagentActive reports whether sub's child is still at work, and returns
// the child. A child is at work while it runs, is driven or finalizes, and
// until its finalize tail releases the delegate: the child stops finalizing
// locally before the tail announces the finished generation, so the result
// may not have reached this session yet.
func (s *Session) subagentActive(sub *subagent) (bool, *Session) {
	sub.mu.Lock()
	active := sub.running || sub.finalizing || sub.driving
	child := sub.sess
	sub.mu.Unlock()
	if !active && child != nil && child.owningDelegateID != "" {
		active = s.delegateController.stillFinalizing(child.owningDelegateID)
	}
	return active, child
}

func (s *Session) hasPendingDrainDelegateDeliveries(liveOnly bool, children []*subagent) bool {
	liveChildren := make(map[string]struct{}, len(children))
	for _, sub := range children {
		liveChildren[sub.sess.id] = struct{}{}
	}
	for _, receiverID := range s.pendingDrainDelegateReceivers(liveChildren) {
		if receiverID == s.id {
			return true
		}
		if s.childStopGated(receiverID) || s.childFatalRunGated(receiverID) || s.childDrainAbandoned(receiverID) {
			continue
		}
		if liveOnly && s.childDrainGracePending(receiverID) {
			continue
		}
		return true
	}
	return false
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
	children := s.liveDirectSubagents()
	if s.hasPendingDrainDelegateDeliveries(true, children) {
		return true, nil
	}
	// A Stop's park defers pending attention to re-engagement, so it must
	// not hold a drain open either.
	if s.isRootDelegateAttentionReceiver() && s.notificationWorkPending() {
		return true, nil
	}
	for _, sub := range children {
		active, child := s.subagentActive(sub)
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id) || s.childDrainGracePending(child.id)) {
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

// delegateAbandonedByDrain reports whether ONE delegate has completed the first
// grace window. It does not itself license abandonment: production records a
// generation-scoped second continuous-stall window and abandons only after that
// whole second window also completes.
//
// EXPLICIT STOP: TIME, NOT ACTIVITY. #378 measured silence instead, and could
// not: every producer of a delegate's activity stamp runs through
// admitLeaseLocked, which rejects on PendingStopSeq != 0, so a stop-requested
// delegate CANNOT report activity at all. The honest first-window question for
// that branch is how long the stop has gone unanswered. A never-stopped
// delegate remains able to report productive activity, so its branch
// deliberately reads that clock and restarts window one when it advances.
//
// NO RUN COMPLETION. currentRunOpen is the "with no run-completion" half: a
// delegate whose run has finished is not wedged, it is done, and its terminal
// packet is deliverable work the drain must still resolve — abandoning it would
// drop a real delivery.
//
// WHY drainStartedAt STANDS IN FOR AN ABSENT REQUEST. In a one-shot run the
// drain begins at the instant the root issued its terminal communicate: the
// root has declared the work over. From that instant the whole delegate subtree
// is treated as stop-requested, which is what brings #317's PRIMARY shape — the
// delegates the root never stopped at all (video-processing xXLyvs9,
// count-dataset-tokens MCJx5Pg, filter-js YUcy8do) — inside this predicate. It
// is also the conservative reading of a journal written before PendingStopAt
// existed, whose pending stop folds with a zero timestamp: unknown becomes "no
// earlier than this drain", never "long ago".
//
// For an explicit pending stop, window one starts at the durable request time.
// For a never-stopped delegate it starts at the one-shot drain boundary and any
// later admitted productive activity restarts it. Stop-requested rows cannot
// admit activity (the controller fence above), so those two branches are
// intentionally distinct.
func delegateAbandonedByDrain(row delegateSnapshot, now, drainStartedAt time.Time) bool {
	if !row.currentRunOpen {
		return false
	}
	since := row.pendingStopAt
	if since.IsZero() {
		since = drainStartedAt
		if row.latestProductiveActivityAt.After(since) {
			since = row.latestProductiveActivityAt
		}
	}
	if since.IsZero() {
		return false
	}
	return !now.Before(since.Add(DrainStallTimeout))
}

// markDrainAbandonedDelegates advances the two-window policy across this session
// and every live descendant. First-window completion records a generation-
// scoped grace phase; only a later pass strictly beyond one full second window
// records abandonment and announces it.
//
// It is what makes the verdict a RECORD rather than a repeated computation.
// #378 asked the question per predicate call, which cost a deep-clone
// Snapshot() per child per call at 4 Hz and — worse — let the drain's liveness
// check, its sole-reason check and its drive paths disagree about the same
// child. Marking once per pass gives every reader one answer for the cost of
// one snapshot per session per pass.
//
// The one-shot fallback is gated on TurnEndsProcess. Under serve the session
// outlives the turn, so an unstopped delegate is nobody's shutdown problem; an
// explicit stop request is different, because the caller has already declared
// that delegate's work over and the interactive drain must not wait forever.
// DrainJobTree runs for nested delegates too, so this distinction applies at
// every level without abandoning work a user has not stopped.
//
// A final mark is monotone only for its captured generation. Activity clears an
// armed never-stopped phase, terminal evidence clears every phase, and a resumed
// generation does not inherit either phase or final mark. A terminal packet that
// arrives from an abandoned generation anyway is still delivered — deliveries
// land in the PARENT's own pending set, which this skip never hides.
func (s *Session) markDrainAbandonedDelegates(now, drainStartedAt time.Time) {
	if s == nil {
		return
	}
	rows := make(map[string]delegateSnapshot)
	for _, visible := range stableDelegateRowsForSession(s, false) {
		if childID := visible.snapshot.descriptor.ChildSessionID; childID != "" {
			rows[childID] = visible.snapshot
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil {
			continue
		}
		row, tracked := rows[child.id]
		// Already given up on, or already skipped by every walk: a gated child's
		// subtree is nobody's reason to wait, so announcing an abandonment
		// underneath it would report giving up on something already given up on.
		// The gate is read off the row this pass already holds, not by
		// re-snapshotting the controller for each question.
		if s.childDrainAbandoned(child.id) || s.childFatalRunGated(child.id) || tracked && delegateRowStopGated(row) {
			s.clearDrainGraceChild(child.id)
			continue
		}
		if !tracked || (!s.cfg.TurnEndsProcess && row.pendingStopSeq == 0) || !delegateAbandonedByDrain(row, now, drainStartedAt) {
			s.clearDrainGraceChild(child.id)
			child.markDrainAbandonedDelegates(now, drainStartedAt)
			continue
		}
		phase, armed := s.drainGraceChildForRow(child.id, row)
		if !armed {
			s.recordDrainGraceChild(child.id, row, now)
			continue
		}
		if row.latestProductiveActivityAt.After(phase.latestProductiveActivityAt) {
			// A never-stopped delegate made productive progress. Restart window
			// one at that progress stamp rather than carrying a stale phase forward.
			s.clearDrainGraceChild(child.id)
			continue
		}
		if now.After(phase.secondWindowStartedAt.Add(DrainStallTimeout)) {
			s.clearDrainGraceChild(child.id)
			s.recordDrainAbandonedChild(child.id, row.id, row.generation)
			continue
		}
	}
}

func (s *Session) recordDrainGraceChild(childSessionID string, row delegateSnapshot, now time.Time) {
	s.drainAbandonedMu.Lock()
	defer s.drainAbandonedMu.Unlock()
	if s.drainGraceChildren == nil {
		s.drainGraceChildren = make(map[string]drainGraceChild)
	}
	s.drainGraceChildren[childSessionID] = drainGraceChild{
		delegateID: row.id, generation: row.generation,
		latestProductiveActivityAt: row.latestProductiveActivityAt, secondWindowStartedAt: now,
	}
}

func (s *Session) clearDrainGraceChild(childSessionID string) {
	s.drainAbandonedMu.Lock()
	delete(s.drainGraceChildren, childSessionID)
	s.drainAbandonedMu.Unlock()
}

func (s *Session) drainGraceChildForRow(childSessionID string, row delegateSnapshot) (drainGraceChild, bool) {
	s.drainAbandonedMu.Lock()
	defer s.drainAbandonedMu.Unlock()
	phase, ok := s.drainGraceChildren[childSessionID]
	return phase, ok && phase.delegateID == row.id && phase.generation == row.generation
}

func (s *Session) childDrainGracePending(childSessionID string) bool {
	if s == nil || childSessionID == "" {
		return false
	}
	row, ok := s.directStableDelegateForChildSession(childSessionID)
	if !ok || !row.currentRunOpen {
		return false
	}
	_, pending := s.drainGraceChildForRow(childSessionID, row)
	return pending
}

func (s *Session) treeHasDrainGracePending() bool {
	if s == nil {
		return false
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil {
			continue
		}
		if s.childDrainGracePending(child.id) || child.treeHasDrainGracePending() {
			return true
		}
	}
	return false
}

// restartDrainGraceSecondWindows preserves completed first windows but requires
// a fresh full second window after genuine drain progress (a delivered
// notification turn). Delegate activity is stronger evidence and clears the
// phase entirely in markDrainAbandonedDelegates above.
func (s *Session) restartDrainGraceSecondWindows(now time.Time) {
	if s == nil {
		return
	}
	s.drainAbandonedMu.Lock()
	for id, phase := range s.drainGraceChildren {
		phase.secondWindowStartedAt = now
		s.drainGraceChildren[id] = phase
	}
	s.drainAbandonedMu.Unlock()
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child != nil {
			child.restartDrainGraceSecondWindows(now)
		}
	}
}

func (s *Session) clearDrainGraceTree() {
	if s == nil {
		return
	}
	s.drainAbandonedMu.Lock()
	clear(s.drainGraceChildren)
	s.drainAbandonedMu.Unlock()
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child != nil {
			child.clearDrainGraceTree()
		}
	}
}

// recordDrainAbandonedChild stamps one abandonment and announces it. The
// announcement is here rather than at the drain's return because the return
// path varies: once the last live component is abandoned the subtree is no
// longer outstanding at all, and the drain quiesces normally instead of
// reaching the stall watchdog's give-up. Warning at the decision point is the
// only placement that reports the abandonment on every path.
func (s *Session) recordDrainAbandonedChild(childSessionID, delegateID string, generation uint64) {
	s.drainAbandonedMu.Lock()
	if s.drainAbandonedChildren == nil {
		s.drainAbandonedChildren = make(map[string]drainAbandonedChild)
	}
	previous, already := s.drainAbandonedChildren[childSessionID]
	s.drainAbandonedChildren[childSessionID] = drainAbandonedChild{delegateID: delegateID, generation: generation}
	s.drainAbandonedMu.Unlock()
	if already && previous.delegateID == delegateID && previous.generation == generation {
		return
	}
	s.emit(events.EventWarning, events.WarningData{
		Message:    fmt.Sprintf("no longer waiting on delegate %s after two drain grace windows with its run still open", delegateID),
		Source:     "drain",
		Code:       events.WarningCodeDelegateAbandonedByDrain,
		DelegateID: delegateID,
	})
}

// childDrainAbandoned reports whether the one-shot drain has given up waiting on
// the direct child session childSessionID. Every consumer — the liveness check,
// the outstanding-work check the undisposed-job announcement's sole-reason test
// reads, the drive paths, and the give-up warning — asks this one question, so
// the drain cannot declare a child dead while still driving it on producer wakes.
func (s *Session) childDrainAbandoned(childSessionID string) bool {
	if s == nil || childSessionID == "" {
		return false
	}
	row, ok := s.directStableDelegateForChildSession(childSessionID)
	if !ok {
		return false
	}
	s.drainAbandonedMu.Lock()
	defer s.drainAbandonedMu.Unlock()
	abandoned, exists := s.drainAbandonedChildren[childSessionID]
	return exists && abandoned.generation == row.generation
}

// subtreeUndisposableStoppedDelegates names the delegates this session and its
// live, non-gated descendants have been given up on, so the stall watchdog's
// give-up can say what it abandoned rather than reporting an empty set of stuck
// managed jobs. Sorted, so the warning text is stable.
func (s *Session) subtreeUndisposableStoppedDelegates() []string {
	var ids []string
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil || s.childStopGated(child.id) || s.childFatalRunGated(child.id) {
			continue
		}
		if s.childDrainAbandoned(child.id) {
			s.drainAbandonedMu.Lock()
			ids = append(ids, s.drainAbandonedChildren[child.id].delegateID)
			s.drainAbandonedMu.Unlock()
			continue
		}
		ids = append(ids, child.subtreeUndisposableStoppedDelegates()...)
	}
	sort.Strings(ids)
	return ids
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
		if child == nil || s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id) || s.childDrainGracePending(child.id) {
			continue
		}
		ids = append(ids, child.subtreeOutstandingDrainJobIDs()...)
	}
	return ids
}

// drainStallGiveUpMessage renders the stall watchdog's give-up warning. It
// names both kinds of thing the drain can be left holding — managed jobs the
// machinery never delivered, and delegates that were told to stop and never
// answered — and says "none" for an empty set rather than trailing off after a
// colon, so the warning can never read as if it gave up on nothing.
func drainStallGiveUpMessage(stuckJobs, undisposableDelegates []string) string {
	describe := func(ids []string) string {
		if len(ids) == 0 {
			return "none"
		}
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf(
		"job-tree drain stalled for %s with no live work; giving up so shutdown can proceed (stuck managed jobs: %s; undisposable stop-requested delegates: %s)",
		DrainStallTimeout, describe(stuckJobs), describe(undisposableDelegates))
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
func (s *Session) DrainJobTree(ctx context.Context, wake <-chan struct{}) (string, error) {
	return s.drainJobTreeUsing(ctx, wake, s.ProcessInputKind)
}

// DrainJobTreeKeepingReport is DrainJobTree for a delegate that has already
// reported: a drain turn that ends with a terminal communicate replaces the
// report, and a drain turn that ends without one leaves the report standing.
func (s *Session) DrainJobTreeKeepingReport(ctx context.Context, wake <-chan struct{}) (string, error) {
	return s.drainJobTreeUsing(ctx, wake, s.keepingReport(s.ProcessInputKind))
}

func (s *Session) drainJobTreeUsing(ctx context.Context, wake <-chan struct{}, process func(context.Context, string, []ImageAttachment, EntryKind) (string, error)) (string, error) {
	if s.jobManager == nil {
		return "", nil
	}
	return s.drainJobTreeWith(ctx, wake, nil, s.kickDriveTree, process)
}

// keepingReport wraps a drain turn. Every input starts with a fresh
// communicate result, so a turn that ends without a terminal communicate would
// erase the report the delegate already gave. That turn restores the latest
// report the session accepted, which may have come from an earlier input of
// the same call. Its bare-text or empty-response
// failure is the reply of a housekeeping turn, not a failure of the run that
// already reported; any other error still propagates.
func (s *Session) keepingReport(process func(context.Context, string, []ImageAttachment, EntryKind) (string, error)) func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
	return func(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (string, error) {
		reported := s.Communicated()
		res, err := process(ctx, input, images, kind)
		if !reported {
			return res, err
		}
		s.mu.Lock()
		if !s.comm.called {
			s.comm = s.latestReport
			if isNoReportFailure(err) {
				err = nil
			}
		}
		s.mu.Unlock()
		return res, err
	}
}

// isNoReportFailure reports whether err consists only of the failures of a
// turn that ended without a terminal communicate. errors.Is alone would also
// match a joined error that carries an unrelated failure beside one of them.
func isNoReportFailure(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		for _, part := range parts {
			if !isNoReportFailure(part) {
				return false
			}
		}
		return len(parts) > 0
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return isNoReportFailure(wrapped.Unwrap())
	}
	return errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted)
}

// drainJobTree exposes an injected resource-deadline edge for deterministic
// tests. Production arms only actual deadlines, never a recheck ticker.
func (s *Session) drainJobTree(ctx context.Context, wake <-chan struct{}, recheck <-chan time.Time) (string, error) {
	return s.drainJobTreeWith(ctx, wake, recheck, s.kickDriveTree, s.ProcessInputKind)
}

func (s *Session) drainJobTreeWith(ctx context.Context, wake <-chan struct{}, recheck <-chan time.Time, kick func(context.Context) error, process func(context.Context, string, []ImageAttachment, EntryKind) (string, error)) (string, error) {
	if hook := s.cfg.testOnly.jobTreeDrainEntered; hook != nil {
		hook()
	}
	defer func() {
		if hook := s.cfg.testOnly.jobTreeDrainReturned; hook != nil {
			hook()
		}
	}()
	s.clearDrainGraceTree()
	defer s.clearDrainGraceTree()
	lastResult := ""
	var stallStart time.Time
	s.repairNotificationReceipts()
	// drainStartedAt is the instant the root declared the work over: in a
	// one-shot run this loop starts the moment the coordinator's terminal
	// communicate ended its turn. It is the reference delegateAbandonedByDrain
	// uses for a delegate nobody stopped — see its doc.
	drainStartedAt := s.sclock().Now()
	// bgAnnounced counts undisposed-background-job announcements per job SET
	// (sorted ids joined). Keying on the set, not a bool, is what lets a job
	// started during an announcement turn get its own full escalation instead of
	// inheriting a spent count — and what stops a stop-and-relaunch from being
	// nagged as if it had already been warned.
	//
	// bgArmed is the set observed by the previous pass that went on to block in
	// the resource-deadline wait: the FIRST announcement fires only when the condition
	// survives one park. A job that is finishing right now — the ordinary
	// launch-background-and-end-turn handoff — finalizes during that park and
	// its completion wake delivers a notification turn instead, so it is never
	// announced; the background-disposition deadline handles a genuinely
	// undisposed job without periodically pumping notifications.
	bgAnnounced := make(map[string]int)
	bgArmed := ""
	watchSuppressed := ""
	watchHistorySeen := s.jobManager.watchHistoryIDs()
	// Only entry and real producer hints fan out source repair.
	wakePending := true
	for {
		var dispositionDeadline time.Time
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
		woke := takeDrainWake(wake) || wakePending
		wakePending = false
		// Take this pass's abandonment verdict BEFORE the kick below, so a
		// child this pass gives up on is not also driven by this same pass.
		// One snapshot per session per pass serves every reader after it.
		s.markDrainAbandonedDelegates(s.sclock().Now(), drainStartedAt)
		if woke {
			if err := kick(ctx); err != nil {
				return lastResult, err
			}
		}
		// A steer admitted while this delegate's run is parked here must not
		// wait for the drain to end on its own (#2796): an owned background job
		// can hold the drain open indefinitely, and when nothing else is queued
		// no notification turn will carry the admission, so the caller's
		// "steered" would be a silent no-op. Stop draining and let the run take
		// its steering continuation, whose model request binds and drains the
		// admission. The steer is consumed before the run re-enters this drain,
		// so the early return does not spin.
		//
		// A queued notification (or root attention, or a child's report/update)
		// already runs a turn whose model request binds the steering with the
		// lease context, so the gate below consumes it and the long-standing
		// notification/steering merge is preserved; only the no-other-driver
		// case returns early.
		//
		// Parked attention does not drive the rung: a Stop's park defers it,
		// and the notification turn would only stand down at the admission
		// gate and spin the drain loop at full rate. A notification to
		// this delegate does drive it (hasPendingOwnDelegateAttention), and
		// the turn that presents it consumes it. The drain runs under the
		// run's lease, so the admission gate (coversPresentedAttention) and
		// this predicate agree. A successful leased turn credits what its
		// requests presented as consumed, so the predicate falls quiet; a
		// notification stays pending only if no request presented it or its marker
		// write failed (retried after the turn), and a turn that fails first
		// returns its error from the process call below rather than looping.
		// Compaction does not hide a pending notification from that turn: it retains
		// unresolved attention turns in the history (retainDelegateAttentionTurn,
		// and resume restores them; see
		// TestRootDelegateAttention_PendingContentSurvivesCompactionRestart),
		// so a request after a compaction or a failed turn still presents it.
		if s.notificationExecutionState().held {
			return lastResult, nil
		}
		notificationTurnOwed := s.peekNotifications() > 0 || s.notificationsRunnable()
		if s.hasPendingStableSteering() && !notificationTurnOwed {
			return lastResult, nil
		}
		if notificationTurnOwed {
			// A completion is queued on this (root) rail: run a notification turn so
			// the coordinator's model receives it and can dispatch more work or wrap
			// up. The turn's boundary also drives any idle descendant that has
			// undelivered attention. The turn's internal loop drains any further
			// already-pending notifications, including frames produced before the
			// final answer but not yet observed by its request.
			res, err := process(ctx, "", nil, EntryNotification)
			if err != nil {
				return lastResult, err
			}
			if res != "" {
				lastResult = res
			}
			s.repairNotificationReceipts()
			s.restartDrainGraceSecondWindows(s.sclock().Now())
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
			// Fence future registrations only once owned resources have settled.
			// Entered callbacks finish source persistence before the confirming scan.
			if s.cfg.TurnEndsProcess {
				if err := s.jobManager.fenceTerminalWatchProduction(ctx); err != nil {
					return lastResult, err
				}
			}
			if takeDrainWake(wake) || s.peekNotifications() > 0 || s.notificationsRunnable() || s.jobManager.hasPendingWatchSends() {
				wakePending = true
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
		// The announcement turns are housekeeping, so their replies do not fold
		// into lastResult (a run's printed answer must not become "I stopped
		// job_2"), and their errors never fail the drain — a provider error on
		// a housekeeping turn must not convert a successful run into a failed
		// one. A failed turn still advances the escalation: the alternative is
		// retrying a broken provider forever with the process held open.
		//
		// One exception, decided per turn: a job that finishes between this
		// pass's queue gate above and the turn's own queue drain
		// (acceptNotificationInput) has its completion queued in time to ride
		// the announcement request. The model then answers the completion, and
		// that reply is the run's answer exactly as a notification turn's is —
		// discarding it printed the pre-drain answer and lost the real one.
		// Successful canonical receipts tell the two apart: the reply is kept
		// only when this input observed a notification for the first time.
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
				// to the resource deadline wait. A completion in flight beats that deadline
				// and is delivered instead of announced.
				bgArmed = setKey
				dispositionDeadline = s.sclock().Now().Add(backgroundDispositionDelay)
			} else {
				switch bgAnnounced[setKey] {
				case 0, 1:
					// The verdict above was assembled across sequential reads. A
					// wake raised since this pass took its edge means a completion
					// finalized meanwhile and is deliverable now: re-run the pass
					// so it is delivered instead of announced.
					if takeDrainWake(wake) {
						wakePending = true
						continue
					}
					canDetach := s.detachedShellAvailable()
					var text string
					if bgAnnounced[setKey] == 0 {
						text = undisposedBackgroundJobsAnnouncement(
							undisposed, s.providerVisibleToolName("shell"), canDetach)
					} else {
						text = undisposedBackgroundJobsFinalWarning(undisposed, canDetach)
					}
					bgAnnounced[setKey]++
					// EntryNotification, not the steering carrier: Steer enqueues
					// DAEMON-sourced steering, and the carrier's entry gate counts
					// only user-sourced steering, so a carrier turn stands down
					// before any model call — which is exactly how an earlier
					// attempt at this shipped inert.
					if err := s.SteerKind(text, events.SteeringKindNotification); err != nil {
						bgAnnounced[setKey]--
						s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("job announcement admission failed: %v", err)})
						return lastResult, err
					}
					observedBefore, err := s.drainObservedNotifications()
					if err != nil {
						return lastResult, err
					}
					res, perr := process(ctx, "", nil, EntryNotification)
					if perr != nil {
						s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
							"undisposed-background-job announcement turn failed: %v", perr)})
					} else if res != "" {
						newlyObserved, err := s.drainHasNewObservation(observedBefore)
						if err != nil {
							return lastResult, err
						}
						if newlyObserved {
							// This input earned a successful canonical receipt,
							// not just custody or repair of an older receipt.
							lastResult = res
						}
					}
					stallStart = time.Time{}
					// A housekeeping turn ran; re-evaluate its resulting work.
					continue
				default:
					// Told twice, declined twice. Same wake-edge protocol as every
					// other return that lets Close() cancel the subtree: a wake
					// raised mid-scan means the tree moved, so re-run the pass
					// rather than killing work that armed while this pass looked.
					if takeDrainWake(wake) {
						wakePending = true
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
		// give up once it exceeds DrainStallTimeout so Close() can proceed. Live work
		// (a running managed job, a driving child, a pending watch send, a queued
		// notification) resets the stall clock, so legitimate long work is never cut.
		stalled, err := s.drainSubtreeIsStalled()
		if err != nil {
			return lastResult, err
		}
		if !stalled || s.treeHasDrainGracePending() {
			// A delegate's explicit second window owns this interval. Do not let
			// the generic stranded-work watchdog return at its inclusive boundary
			// without recording the structured delegate abandonment.
			stallStart = time.Time{}
		} else {
			now := s.sclock().Now()
			if stallStart.IsZero() {
				stallStart = now
			} else if now.Sub(stallStart) >= DrainStallTimeout {
				// The stall verdict was assembled across sequential reads, not a
				// snapshot. A wake raised since this pass took its edge means the
				// tree moved mid-verdict — the same straddle the quiescence return
				// guards against — so re-run the pass rather than letting Close()
				// SIGKILL work that armed while the scan ran. A genuinely wedged
				// tree raises no wake, so the confirming pass gives up cleanly.
				if takeDrainWake(wake) {
					wakePending = true
					continue
				}
				// The drain is wedged on undelivered work the machinery is not
				// converting. Warn (naming the stuck managed job(s) and any delegate
				// abandoned as undisposable) and return the last result with nil error
				// so cmd/evener/run.go prints the coordinator's last answer and
				// proceeds to Close(), rather than aborting the run.
				s.emit(events.EventWarning, events.WarningData{Message: drainStallGiveUpMessage(
					s.subtreeOutstandingDrainJobIDs(), s.subtreeUndisposableStoppedDelegates())})
				return lastResult, nil
			}
		}
		deadline := s.nextDrainResourceDeadline(drainStartedAt)
		for _, candidate := range []time.Time{dispositionDeadline, stallStartDeadline(stallStart)} {
			if !candidate.IsZero() && (deadline.IsZero() || candidate.Before(deadline)) {
				deadline = candidate
			}
		}
		var timer interface{ Stop() bool }
		var resourceDeadline <-chan time.Time
		if !deadline.IsZero() {
			t := s.sclock().NewTimer(deadline.Sub(s.sclock().Now()))
			timer, resourceDeadline = t, t.C()
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return lastResult, ctx.Err()
		case <-wake:
			wakePending = true
		case <-resourceDeadline:
		case <-recheck:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// drainObservedNotifications snapshots canonical successful-response receipts.
// Clone the lifetime fold too, so an empty-StateDir input cannot mutate its own
// before snapshot. Derived resolution/index repair is not a new observation.
func (s *Session) drainObservedNotifications() (map[string]struct{}, error) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	return maps.Clone(fold.observed), nil
}

// drainHasNewObservation compares the canonical fold under its lock, without
// copying or returning the borrowed observed map. The before snapshot is owned.
func (s *Session) drainHasNewObservation(before map[string]struct{}) (bool, error) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return false, err
	}
	for id := range fold.observed {
		if _, earlier := before[id]; !earlier {
			return true, nil
		}
	}
	return false, nil
}

// rematerializeDurablePendings transfers this session's owned durable-only
// shell completions to common receiver custody. The existing custody path owns
// source acknowledgement, retry and lifecycle fencing. Repeated recovery uses
// the same identity, including when custody survived before its acknowledgement.
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
		if _, _, err := jm.persistStableShellAttention(rec.JobID); err != nil {
			return err
		}
	}
	return nil
}

func stallStartDeadline(start time.Time) time.Time {
	if start.IsZero() {
		return time.Time{}
	}
	return start.Add(DrainStallTimeout)
}

// nextDrainResourceDeadline arms the existing two-window delegate policy, not
// a notification retry. Productive activity and resource release raise hints.
func (s *Session) nextDrainResourceDeadline(start time.Time) time.Time {
	var next time.Time
	consider := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	rows := make(map[string]delegateSnapshot)
	for _, visible := range stableDelegateRowsForSession(s, false) {
		if childID := visible.snapshot.descriptor.ChildSessionID; childID != "" {
			rows[childID] = visible.snapshot
		}
	}
	// Match markDrainAbandonedDelegates: only resident children can advance a
	// grace window. A cold open row (for example, ownership-refused on restore)
	// has no such deadline owner, so its expired first window must not re-arm.
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil {
			continue
		}
		row, tracked := rows[child.id]
		if tracked && row.currentRunOpen && !s.childDrainAbandoned(child.id) && !s.childFatalRunGated(child.id) && !delegateRowStopGated(row) && (s.cfg.TurnEndsProcess || row.pendingStopSeq != 0) {
			if phase, ok := s.drainGraceChildForRow(child.id, row); ok {
				consider(phase.secondWindowStartedAt.Add(DrainStallTimeout).Add(time.Nanosecond))
			} else {
				since := row.pendingStopAt
				if since.IsZero() {
					since = start
					if row.latestProductiveActivityAt.After(since) {
						since = row.latestProductiveActivityAt
					}
				}
				consider(since.Add(DrainStallTimeout))
			}
		}
		if !s.childStopGated(child.id) && !s.childFatalRunGated(child.id) && !s.childDrainAbandoned(child.id) {
			consider(child.nextDrainResourceDeadline(start))
		}
	}
	return next
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

// kickDriveTree repairs producer custody throughout the live managed-job
// subtree. Each destination schedules its own execution. This walk never chooses
// a child model executor, and skips lifecycle-fenced subtrees.
func (s *Session) kickDriveTree(ctx context.Context) error {
	return s.kickDriveTreeWith(ctx, func(sess *Session, ctx context.Context) error {
		return sess.drainPendingWatchSends(ctx)
	})
}

func (s *Session) kickDriveTreeWith(ctx context.Context, drain func(*Session, context.Context) error) error {
	if err := s.replayDelegateDeliveries(); err != nil {
		return err
	}
	s.delegateController.recheckNotificationReceivers()
	if err := s.rematerializeDurablePendings(); err != nil {
		return err
	}
	if err := drain(s, ctx); err != nil {
		return err
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		child := sub.sess
		sub.mu.Unlock()
		if child == nil || s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id) {
			continue
		}
		if err := child.kickDriveTreeWith(ctx, drain); err != nil {
			return err
		}
	}
	return nil
}

// repairNotificationReceipts retries only the derived receipt index. Synced
// assistant responses already own observation and never require another call.
func (s *Session) repairNotificationReceipts() {
	if err := s.finishAttentionTurn(nil, nil); err != nil {
		s.emit(events.EventWarning, warningDataFromError("delegate notification receipt index repair failed", err))
	}
}
