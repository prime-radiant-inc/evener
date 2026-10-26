package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
//     (driveStableDelegateAttention) refused every drive for that child, so a
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
	if s.driveStableDelegateAttention(sub) {
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
	row, ok := s.directStableDelegateForChildSession(childSessionID)
	return ok && row.resumable
}

func (s *Session) directStableDelegateForChildSession(childSessionID string) (delegateSnapshot, bool) {
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
	row, ok := s.directStableDelegateForChildSession(childSessionID)
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
