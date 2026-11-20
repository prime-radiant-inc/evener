package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// drainPendingWatchSends is the ONLY executor of watch-send delivery. Call it
// solely from loop-owned code: never from event observation, never under
// responseSideEffectsMu (spec §3/§4.2). A wake submits an EntryNotification; an
// all-token batch renders and settles in the notification turn; an EMPTY accept
// hits finishNotificationNoop → finishProcessingAtBoundary → here, so
// delegate-only wakes deliver with zero model turns.
type watchSendDrainResult struct {
	observerHandoff bool
}

func (s *Session) drainPendingWatchSends(ctx context.Context) error {
	_, err := s.drainPendingWatchSendsReport(ctx)
	return err
}

func (s *Session) drainPendingWatchSendsReport(ctx context.Context) (watchSendDrainResult, error) {
	var result watchSendDrainResult
	var errs []error
	if s.jobManager != nil {
		drained, err := s.drainJobManagerWatchSends(ctx, s.jobManager, "")
		result.observerHandoff = result.observerHandoff || drained.observerHandoff
		errs = append(errs, err)
	}
	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			// sessions filters nil subagents and sessions before returning.
			if child.jobManager == nil {
				continue
			}
			drained, err := child.drainJobManagerWatchSends(ctx, child.jobManager, child.id)
			result.observerHandoff = result.observerHandoff || drained.observerHandoff
			errs = append(errs, err)
		}
	}
	s.driveChildrenWithUndeliveredAttention()
	return result, errors.Join(errs...)
}

// driveChildrenWithUndeliveredAttention re-purposes the parent's loop-boundary
// child traversal as a DRIVE-SIGNAL reader (spec §3): for each LIVE, IDLE direct
// child with undelivered attention, the parent launches ONE EntryNotification
// turn on the child's own drain loop. This is signal-reading only: it renders no
// notification on the parent's rail (worker terminals reach the child's own
// model, never the parent's — spec §3). The drive turn is async
// (driveSubagentNotificationTurn fires a goroutine), so the parent's boundary
// never blocks on a child's loop.
//
// Drive signals (spec §3): (a) queued job notifications (peekNotifications) and
// (b) pending caller-targeted watch sends (hasPendingWatchSends). Signal (b) is
// read here now that the v2 child-iteration re-route is deleted — a mid-owner
// caller frame renders in the mid's own drive turn instead of being re-tokened
// onto the parent's rail. A deliberately stopped child is stop-gated (no
// resurrection); a successful handoff settles the parent's forwarded drive
// signal; a child that cannot be driven escalates as "child unreachable:".
func (s *Session) driveChildrenWithUndeliveredAttention() {
	live := make(map[string]bool)
	for _, sub := range s.liveDirectSubagents() {
		child := sub.sess
		// liveDirectSubagents filters nil subagents and sessions.
		live[child.id] = true
		// Stop-gating (spec §3): a deliberately stopped child is never resurrected
		// by a drive for attention that predates the stop. New work clears the gate.
		// A child the one-shot drain has given up on is skipped for the same
		// reason and, additionally, for consistency: the drain declares it not
		// live work, so driving it here would have the drain kicking a child it
		// has already told the operator it abandoned — and abandoning a queued
		// notification the drive loop was mid-way through delivering.
		if s.childDriveGated(child.id) {
			continue
		}
		if child.peekNotifications() > 0 || child.jobManager.hasPendingWatchSends() {
			// LIFE-01 (#2340): the same decision the wake edge and the paced
			// re-drive make, from the same function, so a watch-only queue is
			// skipped here while an ordinary completion keeps its retry.
			if s.driveWouldStrandOnAdmission(child) {
				// Skipping the drive is only half the answer. The notice must
				// leave the queue or the one-shot drain counts it as live work
				// forever and neither quiesces nor times out. Moving it to
				// durable attention keeps the delivery and lets the drain finish.
				child.deferQueuedWatchNoticesToAttention()
				continue
			}
			if s.driveSubagentNotificationTurn(sub) {
				// Settle the parent's forwarded drive signal on a successful
				// handoff so the same stale pending does not re-drive forever; the
				// child's own ledger stays the source of truth.
				s.settleDrivenChildForwardedPendings(child.id)
			}
		}
	}
	s.renderUnreachableChildPendings(live)
}

// redriveLiveChild re-drives the ONE live direct child whose wake a held
// claim or guard dropped, once that claim or guard is let go and no run took
// the dropped wake. Two exits use it:
//   - a committed-send-start rollback (#940): while the claim was held the
//     wake edge (driveChildIfNotStopGated) and the attention primitive
//     (driveDelegateAttention) refused every drive for that child, so a
//     child notification, pending watch send or armed stable attention that
//     landed in the window was DROPPED;
//   - an attention drive that launched no run, or a notification turn, after
//     an attention drive was refused on its drive guard (#3723), such as the
//     finalize tail's re-arm drive landing while a drive refused busy still
//     held the guard.
//
// Nothing that ran drains the dropped wake, so the exit must re-drive it.
// Only that child can hold the dropped wake, so this is scoped to
// childSessionID: a whole-tree sweep would re-drive unrelated children and
// read every child's transcript fold.
//
// It delegates to the shared wake-edge driver driveChildIfNotStopGated so the
// re-drive order cannot drift from the wake edge: stable delegate attention
// FIRST (its run drains the child's notification queue itself), and only the
// notification turn when no attention is owed. The previous inline order drove
// the notification turn first; when both were pending the notification drive
// set sub.driving synchronously, the attention drive then refused on that flag,
// and nothing retried it -- the armed attention stayed stranded. The claim or
// guard is released before this call, so driveChildIfNotStopGated's own
// childDriveGated check is the same stop/fatal/drain gate every other drive
// reads.
func (s *Session) redriveLiveChild(childSessionID string) {
	if s == nil || childSessionID == "" {
		return
	}
	for _, sub := range s.liveDirectSubagents() {
		if sub == nil || sub.sess == nil || sub.sess.id != childSessionID {
			continue
		}
		s.driveChildIfNotStopGated(sub)
		return
	}
}

// stableDelegateChild reports whether sess belongs to a STABLE delegate child: a
// delegate whose runs are exact generations under a controller lease. Ephemeral
// subagents carry no owning delegate id (subagents.go sets it empty), so they are
// not covered and keep the notification-turn fallback they legitimately use.
func stableDelegateChild(sess *Session) bool {
	return sess != nil && sess.owningDelegateID != "" && sess.delegateController != nil
}

// driveWouldStrandOnAdmission reports whether driving this child with an
// EntryNotification turn would be refused by admission, so the drive would take a
// slot, launch a turn that stands straight back down, and settle nothing
// (LIFE-01, #2340).
//
// Every drive site asks THIS function -- the wake edge, the parent's child sweep,
// and the paced re-drive after an ordinary turn -- so no site can bypass the
// decision. The decision is the same structural predicate admission uses: a
// stable child, a watch notice pending, and no ordinary notice for the drive to
// carry. Before it decides, it RESOLVES the queue's caller-targeted watch-send
// tokens (resolveQueuedWatchSendTokens), because a token is not a notice: once
// resolved, the queue holds only the work a turn could actually carry.
//
// Three exemptions keep it from stranding legitimate work:
//
//   - An ordinary notice in the same queue (hasPendingOrdinaryNotification)
//     makes the drive worth launching: acceptance now defers the watch notice
//     and carries the completion (see deferUnleasedWatchNotices), so the turn
//     does real work.
//   - Steering queued for the child makes ANY drive worth launching, including
//     one whose notification queue resolved to nothing. acceptNotificationInput
//     drains steering on its own (injectDrainedSteering), so a turn that carries
//     no notification still presents the steer and does real work. Skipping the
//     drive here would strand the steer: a stable child's steering is run only
//     by a turn some drive launches, and this function is the only gate between
//     the drive and that turn.
//   - A TRANSCRIBED watch-send token whose settle the resolution could not
//     record still needs the drive: its frame is already in the child's
//     transcript and only its delivered record is outstanding, so skipping
//     would strand a settlement the journal is ready to record.
func (s *Session) driveWouldStrandOnAdmission(child *Session) bool {
	if !stableDelegateChild(child) {
		return false
	}
	// Resolve the queue's caller-targeted watch-send tokens BEFORE deciding
	// (LIFE-01, #2340). A stale token names no frame, a transcribed one owes
	// only its delivered record, and an untranscribed one belongs in leased
	// attention -- and any of the three read as an unleased watch notice either
	// strands this drive or, worse, gets presented without a lease.
	child.resolveQueuedWatchSendTokens()
	if !child.hasPendingWatchNotification() {
		if child.peekNotifications() == 0 && (child.jobManager == nil || !child.jobManager.hasPendingWatchSends()) {
			// Resolution left nothing to drive, so the drive is skipped: an
			// empty turn would take a slot only to stand straight back down,
			// and a stale token would otherwise keep the drain counting work
			// no turn can drain.
			//
			// Steering is the exception: it is not a notification, so the
			// resolution above cannot leave it behind, and the turn carries it
			// without one. A park (a Stop the user has not answered) is the one
			// queued shape that turn would NOT carry -- injectDrainedSteering
			// runs but selectDrainNextAction takes goIdle ahead of the
			// autonomous rungs -- so a parked steer does not buy a drive.
			return !child.hasPendingSteering() || child.steeringParkedNow()
		}
		return false
	}
	if child.hasPendingOrdinaryNotification() {
		return false
	}
	return !child.hasSettleableWatchSendOnly()
}

// hasSettleableWatchSendOnly reports whether the only thing this session owes is
// a caller-targeted watch send whose frame is already transcribed, so the
// delivery pass can settle it with no model turn (LIFE-01, #2340).
//
// The queue check above cannot see this case: a transcribed token is re-tokened
// onto the rail and would otherwise read as a watch notice with nothing ordinary
// beside it, which is exactly the shape the drive skip suppresses. Settling it
// needs no lease because it presents nothing to the model.
func (s *Session) hasSettleableWatchSendOnly() bool {
	if s == nil || s.jobManager == nil {
		return false
	}
	jm := s.jobManager
	if !jm.hasPendingWatchSends() {
		return false
	}
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) == 0 {
		return false
	}
	for _, delivery := range pending {
		if delivery.state.DeliveryID == "" || !jm.watchSendTranscribed(delivery.state.DeliveryID) {
			return false
		}
	}
	return true
}

// deferQueuedWatchNoticesToAttention moves every WATCH notice off this session's
// ordinary queue and into durable delegate attention (LIFE-01, #2340). It
// reports how many it moved and how many it could not make durable.
//
// The two counts are separate because a caller deciding whether to refuse work
// must answer for the batch this call took, not for the queue it leaves behind:
// a notice that arrives while the deferral runs is not part of that batch and
// belongs to its own delivery pass.
//
// A drive for a stable child's watch-only queue is refused, and refusing it must
// not leave the notice sitting on a queue that no turn may drain. Two things go
// wrong if it does: the notice is never presented (no unleased turn may carry
// it, and no leased run is ever triggered by a queue it cannot drain), and the
// one-shot drain treats the raw queue as live work, so it neither quiesces nor
// reaches its stall timeout -- the gate would hang the run it was meant to
// protect.
//
// Moving the notice to durable attention fixes both: the queue empties, so the
// drain sees no work it can make progress on and finishes, while the notice
// survives as an item a leased generation presents.
//
// Tokens (WatchSend set) are resolved first, because a token is not a notice
// this queue can carry: the frame it names is either stale, already in the
// transcript, or unpublished, and only the last of those is work. See
// resolveQueuedWatchSendTokens.
func (s *Session) deferQueuedWatchNoticesToAttention() (deferred, failed int) {
	if s == nil {
		return 0, 0
	}
	deferred, failed = s.resolveQueuedWatchSendTokens()
	s.pendingJobNotifsMu.Lock()
	remaining := make([]jobNotification, 0, len(s.pendingJobNotifs))
	var moved []jobNotification
	for _, n := range s.pendingJobNotifs {
		if n.isWatch() && n.WatchSend == nil {
			moved = append(moved, n)
			continue
		}
		remaining = append(remaining, n)
	}
	if len(moved) > 0 {
		s.pendingJobNotifs = remaining
	}
	s.pendingJobNotifsMu.Unlock()
	var retry []jobNotification
	for _, n := range moved {
		// A tick is not a fire on its own (LIFE-01, #2340). A repeating timer can
		// be cleared -- by the operator, by its own teardown -- after this tick was
		// built but before the conversion below, and promoting it would persist
		// and arm a notice for a watch that no longer exists. filterDeliverable-
		// JobNotifications makes this same drop on the accept path; the conversion
		// must not be the one writer that skips it, or a cleared timer could still
		// start a delegate generation. A non-terminal one-shot keeps its tick: the
		// timer retires BECAUSE it fired, so a dead key is its normal state.
		if n.WatchID != "" && !n.Terminal && !s.timerWatchIsLive(n.WatchID) {
			continue
		}
		appended, err := s.persistDeferredWatchNotice(n)
		if err != nil {
			s.emit(warningDataFromError("persist queued watch notice", err))
			if !appended {
				// The APPEND failed, so this notice is NOT durable. Putting it
				// back keeps it owed rather than losing a failure report; the
				// drain's liveness accounting then still sees it, which is
				// honest. A notice whose append succeeded is durable already and
				// is NOT put back: see persistDeferredWatchNotice on why
				// re-offering it presents it twice.
				retry = append(retry, n)
				failed++
				continue
			}
		}
		deferred++
	}
	s.requeueJobNotifications(retry)
	return deferred, failed
}

// driveChildIfNotStopGated is the wake-edge drive: it skips a stop-gated child so
// a deliberately stopped child is not resurrected by its own pre-stop notify
// (spec §3 stop-gating), and a child the one-shot drain has abandoned for the
// same reason driveChildrenWithUndeliveredAttention does. On a successful
// handoff it settles the parent's forwarded drive signal for that child.
func (s *Session) driveChildIfNotStopGated(sub *subagent) {
	if sub == nil || sub.sess == nil {
		return
	}
	if s.childDriveGated(sub.sess.id) {
		return
	}
	if s.driveDelegateAttention(sub) {
		return
	}
	// LIFE-01 (#2340): a stable delegate child has no model-bearing fallback for
	// a WATCH notice. driveDelegateAttention above is the only drive a watch
	// notice may use, because its ingress made it durable attention whose whole
	// point is a governed generation; the notification drive below launches an
	// UNLEASED EntryNotification turn, which the controller would report as an
	// idle child making a provider call. A watch notice reaching this point was
	// demoted to the ordinary queue by some path other than production ingress,
	// so it is refused here as well as at admission rather than run unleased.
	//
	// Every OTHER notice keeps this fallback, and the skip is scoped to a
	// watch-ONLY queue. A terminal shell completion, a nested delegate's
	// forwarded report and the rest are ordinary queued notifications with no
	// lease to inherit, and the queue is drained only by an EntryNotification
	// turn -- so skipping the drive here would strand them forever instead of
	// closing this issue. driveWouldStrandOnAdmission is the one place that
	// decides this, so the paced re-drive cannot disagree with this edge.
	if s.driveWouldStrandOnAdmission(sub.sess) {
		// Same reason as the sweep: the notice leaves the ordinary queue for
		// durable attention, so it is neither presented unleased nor counted as
		// livelock work the drain waits on.
		sub.sess.deferQueuedWatchNoticesToAttention()
		return
	}
	if s.driveSubagentNotificationTurn(sub) {
		s.settleDrivenChildForwardedPendings(sub.sess.id)
	}
}

// renderUnreachableChildPendings is the failure fallback (spec §3): a forwarded
// pending in the parent's store whose owner child cannot be driven — the child is
// not a live direct subagent AND is non-resumable (closed, descriptor-less,
// validation failure) — is rendered by the parent itself, prefixed "child
// unreachable:". Attention escalates one honest level instead of vanishing. live
// is the set of session ids that ARE live direct subagents this pass (those are
// driven, not rendered here). A child that is terminal-but-resumable is left
// pending for its own future drive/resume turn — not falsely escalated.
func (s *Session) renderUnreachableChildPendings(live map[string]bool) {
	if s == nil || s.jobManager == nil {
		return
	}
	jm := s.jobManager
	s.renderUnreachableChildPendingsWithLoaders(live, jm.store.Load, jm.store.LoadWatchSends)
}

func (s *Session) renderUnreachableChildPendingsWithLoaders(
	live map[string]bool,
	loadJobs func() (map[string]*jobstore.JobRecord, error),
	loadWatchSends watchSendLoader,
) {
	jm := s.jobManager
	recs, err := loadJobs()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec == nil ||
			rec.OwnerSessionID == "" ||
			rec.OwnerSessionID == jm.sessionID ||
			rec.VisibleToSession != jm.sessionID ||
			rec.TerminalGen == "" ||
			rec.NotifyState != jobstore.NotifyPending {
			continue
		}
		if live[rec.OwnerSessionID] {
			continue // driven, not rendered
		}
		if s.childResumable(rec.OwnerSessionID) {
			continue // resumable but idle: left for its own future drive/resume turn
		}
		n := jobNotificationFromRecord(rec)
		n.Reason = strings.TrimSpace("child unreachable: " + rec.Reason)
		s.enqueueJobNotification(n)
	}
	watchSends, err := loadWatchSends()
	if err != nil {
		return
	}
	for _, state := range watchSends.Pending {
		if state == nil ||
			state.Key.ResolvedSendTo != runtimeMessageAliasCaller ||
			state.Key.VisibleSessionID == "" ||
			state.Key.VisibleSessionID == jm.sessionID {
			continue
		}
		childSessionID := state.Key.VisibleSessionID
		if live[childSessionID] {
			continue // driven, not rendered
		}
		if s.childResumable(childSessionID) {
			continue // resumable but idle: left for its own future drive/resume turn
		}
		dropped := *state
		dropped.DiagnosticReason = limitWatchText("child unreachable: "+state.TriggerReason, watchReadErrorMaxChars)
		if err := jm.appendWatchSendEvents([]jobstore.Event{{
			Kind:      jobstore.EventWatchSendDropped,
			TS:        jm.now(),
			WatchSend: &dropped,
		}}); err != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("watch-send fallback drop failed: %v", err)})
			continue
		}
		jm.removeRuntimePendingWatchSend(dropped)
		n := watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, dropped.DiagnosticReason)
		n.Provenance = provenance.Clone(state.Provenance)
		s.enqueueJobNotification(n)
	}
}

// childResumable reports whether the stable controller holds a direct child
// descriptor that is currently resumable.
func (s *Session) childResumable(childSessionID string) bool {
	row, ok := s.directDelegateForChildSession(childSessionID)
	return ok && row.resumable
}

func (s *Session) directDelegateForChildSession(childSessionID string) (delegateSnapshot, bool) {
	if s == nil || s.delegateController == nil || childSessionID == "" {
		return delegateSnapshot{}, false
	}
	for _, row := range s.delegateController.snapshotsForChildSession(childSessionID) {
		if delegateRowVisibleTo(s, row.parentID, row.descriptor.OwnerSessionID, row.descriptor.VisibleSessionID) {
			return row, true
		}
	}
	return delegateSnapshot{}, false
}

// childStopGated reports whether the direct stable delegate's latest settled
// generation was deliberately stopped by its parent. A newer running
// generation clears the gate.
func (s *Session) childStopGated(childSessionID string) bool {
	row, ok := s.directDelegateForChildSession(childSessionID)
	return ok && delegateRowStopGated(row)
}

// childCommittedSendStart reports whether childSessionID has a committed send
// start whose owning run has not yet taken the generation over (#940). The
// claim is keyed by child session id, not by the child object, so it covers the
// stretch CommitStart -> restoreIdleForSend -> admitReconstructed/AttachRuntime
// where the child does not exist yet or is still idle and drivable. Every
// wake-edge drivability check reads it, and driveSubagentNotificationTurn reads
// it too, so the drive refuses without needing a resolved child object.
func (s *Session) childCommittedSendStart(childSessionID string) bool {
	if s == nil || childSessionID == "" {
		return false
	}
	s.childCommittedSendMu.Lock()
	defer s.childCommittedSendMu.Unlock()
	_, held := s.childCommittedSendChildren[childSessionID]
	return held
}

// childDriveGated is the drive-gate conjunction every child-attention drive
// consults: a deliberately stopped child, a fatal-run-gated child, a child the
// one-shot drain has abandoned, a child with a drain grace window pending, and a
// child holding a committed-send-start claim are each refused a drive. It lives
// in one place so a new gate cannot be added to some call sites and not others.
func (s *Session) childDriveGated(childSessionID string) bool {
	return s.childStopGated(childSessionID) ||
		s.childFatalRunGated(childSessionID) ||
		s.childDrainAbandoned(childSessionID) ||
		s.childDrainGracePending(childSessionID) ||
		s.childCommittedSendStart(childSessionID)
}

// claimChildCommittedSendStart takes the id-keyed committed-send-start claim.
// It returns false when the claim is already held — a second committed start
// racing on the same delegate — so the caller refuses as busy rather than
// launching a second turn on the session the first start is about to run.
func (s *Session) claimChildCommittedSendStart(childSessionID string) bool {
	if s == nil || childSessionID == "" {
		return false
	}
	s.childCommittedSendMu.Lock()
	defer s.childCommittedSendMu.Unlock()
	if _, held := s.childCommittedSendChildren[childSessionID]; held {
		return false
	}
	if s.childCommittedSendChildren == nil {
		s.childCommittedSendChildren = make(map[string]struct{})
	}
	s.childCommittedSendChildren[childSessionID] = struct{}{}
	return true
}

// releaseChildCommittedSendStart drops the id-keyed committed-send-start claim.
func (s *Session) releaseChildCommittedSendStart(childSessionID string) {
	if s == nil || childSessionID == "" {
		return
	}
	s.childCommittedSendMu.Lock()
	delete(s.childCommittedSendChildren, childSessionID)
	s.childCommittedSendMu.Unlock()
}

// delegateRowStopGated is childStopGated's verdict on a row the caller already
// holds. The drain's abandonment pass takes one snapshot per session per pass
// and asks several questions of each row, so it must not re-snapshot the whole
// controller to ask this one.
func delegateRowStopGated(row delegateSnapshot) bool {
	return delegateOutcomeStopGated(row.lastOutcome, row.currentRunOpen)
}

// settleDrivenChildForwardedPendings marks the parent's forwarded pending COPIES
// for childSessionID delivered at a successful drive handoff (spec §3 settle).
// This is safe because the parent's forwarded copy is only a DRIVE SIGNAL, not
// the delivery ledger: the child's own durable queue (armed at the original
// enqueue, re-armed at the child's restore) is the ledger, and the no-loss clause
// keeps its meaning on the owner's copy. Settling the signal stops the same stale
// pending from re-driving forever; it never touches the child's own store.
func (s *Session) settleDrivenChildForwardedPendings(childSessionID string) {
	if s == nil || s.jobManager == nil || childSessionID == "" {
		return
	}
	jm := s.jobManager
	recs, err := jm.store.Load()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec == nil ||
			rec.OwnerSessionID != childSessionID ||
			rec.VisibleToSession != jm.sessionID ||
			rec.TerminalGen == "" ||
			rec.NotifyState != jobstore.NotifyPending {
			continue
		}
		if err := jm.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          jm.now(),
			JobID:       rec.JobID,
			TerminalGen: rec.TerminalGen,
		}); err != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("drive settle mark failed: %v", err)})
		}
	}
}

// enqueueOwnCallerWatchSendTokens enqueues this session's pending caller-targeted
// watch-send tokens onto its OWN rail. A drive turn launched on the
// hasPendingWatchSends signal (drive signal b) may have no token queued yet; this
// puts the caller frames on the rail so they render in the same drive turn. It is
// caller-only — it does not deliver delegate-targeted observer frames, which the loop
// boundary handles — so it never duplicates a sidecar delivery.
func (s *Session) enqueueOwnCallerWatchSendTokens() {
	if s == nil || s.jobManager == nil || s.jobManager.enqueue == nil {
		return
	}
	for _, delivery := range s.jobManager.pendingWatchSendDeliveries(nil) {
		if delivery.state.Key.ResolvedSendTo != runtimeMessageAliasCaller {
			continue
		}
		s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
	}
}

func (s *Session) retryRestoredPendingWatchSends(_ context.Context) error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	deliveries := s.jobManager.pendingWatchSendDeliveries(nil)
	for _, delivery := range deliveries {
		if delivery.state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
			}
			continue
		}
		if delivery.state.StableReceiver {
			s.jobManager.kick()
			continue
		}
		if err := s.jobManager.dropWatchSend(delivery.state, delivery.cfg, "retired loose watch receiver"); err != nil {
			return err
		}
	}
	return nil
}

// resolveQueuedWatchSendTokens takes every caller-targeted watch-send token off
// this session's notification queue and settles what the token actually owes
// (LIFE-01, #2340).
//
// A token is not a notice. It names a frame by key and sequence, and that name
// resolves to one of three things -- nothing (the frame was replaced, cleared,
// dropped or already settled), a frame the model already has, or a frame nobody
// has presented. Only the last is work, and it is work for a LEASED generation,
// never for the unleased notification turn that would otherwise carry the token.
// Leaving all three on the queue is what went wrong: a stale token kept the
// queue reading as live work so the one-shot drain neither quiesced nor timed
// out, and an untranscribed one let an unleased turn present a watch frame.
//
// Resolution runs before every decision that reads the queue, so the drive and
// the deferral see the work that is really there.
//
// It reports how many tokens it made durable and how many it had to leave owed:
// a token whose frame could not be made durable is requeued, and that is the
// shape admission must still refuse for.
func (s *Session) resolveQueuedWatchSendTokens() (deferred, failed int) {
	if s == nil {
		return 0, 0
	}
	s.pendingJobNotifsMu.Lock()
	if len(s.pendingJobNotifs) == 0 {
		s.pendingJobNotifsMu.Unlock()
		return 0, 0
	}
	remaining := make([]jobNotification, 0, len(s.pendingJobNotifs))
	var tokens []jobNotification
	for _, n := range s.pendingJobNotifs {
		if n.WatchSend != nil {
			tokens = append(tokens, n)
			continue
		}
		remaining = append(remaining, n)
	}
	if len(tokens) == 0 {
		s.pendingJobNotifsMu.Unlock()
		return 0, 0
	}
	s.pendingJobNotifs = remaining
	s.pendingJobNotifsMu.Unlock()
	var retry []jobNotification
	for _, n := range tokens {
		retryToken, madeDurable, tokenFailed := s.resolveQueuedWatchSendToken(n)
		if madeDurable {
			deferred++
		}
		if tokenFailed {
			failed++
		}
		if retryToken {
			retry = append(retry, n)
		}
	}
	s.requeueJobNotifications(retry)
	return deferred, failed
}

// resolveQueuedWatchSendToken settles one token. It reports whether the token
// must go back on the queue (its work could not be finished), and whether it
// became durable attention -- the one outcome a leased generation still owes
// something to present.
//
// A stale token is discarded: it names nothing, so there is nothing to do and
// nothing to retry. A transcribed one is settled in place -- the model already
// has the frame, so only the delivered record is outstanding and no lease is
// needed to write it. An untranscribed one goes to durable attention, where a
// leased generation presents it.
func (s *Session) resolveQueuedWatchSendToken(n jobNotification) (retry, deferred, failed bool) {
	jm, cfg, state, ok := s.resolveWatchSendToken(n.WatchSend)
	if !ok {
		return false, false, false
	}
	if jm.watchSendTranscribed(state.DeliveryID) {
		if err := jm.settleTranscribedWatchSend(cfg, state); err != nil {
			s.emit(events.WarningData{Message: "watch send settle failed: " + err.Error()})
			// The frame is in the transcript, so nothing is presented by the
			// retry and nothing needs making durable: only its delivered record
			// is outstanding. Back on the queue for another settle attempt, but
			// NOT a deferral failure -- refusing work for an already-presented
			// frame would strand the ordinary completions beside it.
			return true, false, false
		}
		return false, false, false
	}
	deferred, err := s.deferWatchSendFrameToAttention(jm, cfg, state)
	if err != nil {
		s.emit(warningDataFromError("defer queued watch send", err))
		return true, false, true
	}
	return false, deferred, false
}

// deferWatchSendFrameToAttention makes one caller-targeted watch frame durable
// attention on this session and acknowledges the send (LIFE-01, #2340).
//
// It is the unleased-turn half of the frame rail: the row goes in under the
// delivery's own identity (stableWatchAttentionID), so a retry of the same
// delivery resolves the same row instead of presenting the frame twice, and
// arming it hands the presentation to a leased generation. Once the row is
// durable the frame IS delivered, so the send is settled: nothing else owes it,
// and leaving it pending would only have another writer present it again.
//
// A failed append returns the error and leaves the send pending, so the caller
// can keep the token owed. A failed arm does not: the row exists and the arm
// retry owns it, so putting the token back would present the frame unleased as
// well as to the leased run the row already waits for.
//
// It reports whether it made a row durable. A frame that is no longer current
// is not presentable, so it reports false and no error: the token names nothing
// and is discarded rather than retried forever.
func (s *Session) deferWatchSendFrameToAttention(jm *jobManager, cfg *watchConfig, state jobstore.WatchSendState) (bool, error) {
	if s == nil || jm == nil || cfg == nil || state.DeliveryID == "" {
		return false, errors.New("watch send deferral identity is incomplete")
	}
	// The frame may have been cleared or superseded since the token was built.
	// A superseded frame's successor carries the content, and a cleared watch's
	// frame is no longer owed, so neither is presentable.
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return false, nil
	}
	attentionID := stableWatchAttentionID(state)
	if s.delegateController != nil {
		s.delegateController.attentionCauses.remember(attentionID, state.Provenance)
	}
	if _, err := s.appendDelegateNotificationDurably(attentionID, stableWatchNotificationContent(jm.renderWatchSendFrame(cfg, state))); err != nil {
		if s.delegateController != nil {
			s.delegateController.attentionCauses.forget(attentionID)
		}
		return false, err
	}
	// The row is durable, so the frame IS delivered: the leased generation the
	// arm below wakes presents it, and no unleased pass may render it again.
	// Marking it transcribed is what makes that at-most-once, and it has to
	// happen before the arm. The arm wakes the receiver, whose notification
	// drain resolves this very delivery; a delivery still untranscribed is
	// work it hands back to durable attention or presents, either of which
	// appends the frame a second time. Marking it first means a drain that
	// beats the delivered record takes the transcribed path, which settles
	// only, and a failed settle is retried by settleTranscribedWatchSend
	// without rendering.
	jm.noteWatchSendTranscribed(state.DeliveryID)
	if err := s.armDelegateAttention(attentionID); err != nil {
		s.emit(warningDataFromError("arm deferred watch send", err))
	}
	if jm.isCurrentPendingWatchSend(cfg, state) {
		// The transcribed settle is the retry that owns this record now: it
		// acknowledges the frame and, when the journal refuses, backs off and
		// finally settles in memory only. The plain settle would leave the
		// frame pending on every failure, and a later drain would find it
		// transcribed and retry the same way regardless -- so this is the same
		// recovery, taken without re-rendering in between.
		if err := jm.settleTranscribedWatchSend(cfg, state); err != nil {
			s.emit(warningDataFromError("settle deferred watch send", err))
		}
	}
	return true, nil
}
