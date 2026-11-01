package agent

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/internal/delegatestore"
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
		drained, err := s.drainJobManagerWatchSends(ctx, s.jobManager)
		result.observerHandoff = result.observerHandoff || drained.observerHandoff
		errs = append(errs, err)
	}
	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			// sessions filters nil subagents and sessions before returning.
			if child.jobManager == nil {
				continue
			}
			drained, err := child.drainJobManagerWatchSends(ctx, child.jobManager)
			result.observerHandoff = result.observerHandoff || drained.observerHandoff
			errs = append(errs, err)
		}
	}
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

// driveChildIfNotStopGated is the wake-edge drive: it skips a stop-gated child so
// a deliberately stopped child is not resurrected by its own pre-stop notify
// (spec §3 stop-gating), and a child the one-shot drain has abandoned for the
// same reason driveChildrenWithUndeliveredAttention does. On a successful
// handoff it settles the parent's forwarded drive signal for that child.

// renderUnreachableChildPendings is the failure fallback (spec §3): a forwarded
// pending in the parent's store whose owner child cannot be driven — the child is
// not a live direct subagent AND is non-resumable (closed, descriptor-less,
// validation failure) — is rendered by the parent itself, prefixed "child
// unreachable:". Attention escalates one honest level instead of vanishing. live
// is the set of session ids that ARE live direct subagents this pass (those are
// driven, not rendered here). A child that is terminal-but-resumable is left
// pending for its own future drive/resume turn — not falsely escalated.

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
	if !delegateRunTerminal(row.lastOutcome, row.currentRunOpen) {
		return false
	}
	return row.lastOutcome.Status == delegatestore.OutcomeStopped && row.lastOutcome.Reason == delegatestore.ReasonStoppedByParent
}

// settleDrivenChildForwardedPendings marks the parent's forwarded pending COPIES
// for childSessionID delivered at a successful drive handoff (spec §3 settle).
// This is safe because the parent's forwarded copy is only a DRIVE SIGNAL, not
// the delivery ledger: the child's own durable queue (armed at the original
// enqueue, re-armed at the child's restore) is the ledger, and the no-loss clause
// keeps its meaning on the owner's copy. Settling the signal stops the same stale
// pending from re-driving forever; it never touches the child's own store.

// enqueueOwnCallerWatchSendTokens enqueues this session's pending caller-targeted
// watch-send tokens onto its OWN rail. A drive turn launched on the
// hasPendingWatchSends signal (drive signal b) may have no token queued yet; this
// puts the caller frames on the rail so they render in the same drive turn. It is
// caller-only — it does not deliver delegate-targeted observer frames, which the loop
// boundary handles — so it never duplicates a sidecar delivery.

func (s *Session) retryRestoredPendingWatchSends(_ context.Context) error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	return s.retryRestoredPendingWatchSendsFrom(s.jobManager.store.LoadWatchSends)
}

func (s *Session) retryRestoredPendingWatchSendsFrom(load watchSendLoader) error {
	deliveries := s.jobManager.pendingWatchSendDeliveries(nil)
	// Canonical source-receipt work can remain after the current head settles.
	dispatch := s.jobManager.retirementOwner != nil || s.jobManager.delegateController != nil
	for _, delivery := range deliveries {
		if delivery.state.StableReceiver || delivery.state.CallbackFrame || delivery.state.ReceiverSessionID != "" {
			dispatch = true
			continue
		}
		if err := s.jobManager.dropWatchSend(delivery.state, delivery.cfg, "retired loose watch receiver"); err != nil {
			return err
		}
	}
	if dispatch {
		return s.jobManager.dispatchProducedWatchSendsFrom(load)
	}
	return nil
}
