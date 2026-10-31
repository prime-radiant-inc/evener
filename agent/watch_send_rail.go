package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

func (jm *jobManager) snapshotWatchSendFrames(deliveries []watchSendDelivery) []watchSendDelivery {
	for i := range deliveries {
		deliveries[i] = jm.snapshotWatchSendFrame(deliveries[i])
	}
	return deliveries
}

func (jm *jobManager) snapshotWatchSendFrame(d watchSendDelivery) watchSendDelivery {
	if d.send == nil {
		return d
	}
	d.message = limitWatchText(strings.TrimSpace(d.send.Message), watchMessageMaxChars)
	watchID := ""
	generation := ""
	note := ""
	if d.cfg != nil {
		watchID = d.cfg.watchID
		generation = d.cfg.generation
		note = d.cfg.note
	}
	d.frame = jm.buildWatchFrame(&watchConfig{
		watchID:    watchID,
		generation: generation,
		note:       note,
		send:       d.send,
	}, d.watchedIdentity, d.trigger, d.deliveryID, events.SessionEvent{
		Kind:       d.eventKind,
		SessionID:  jm.sessionID,
		Data:       d.eventData,
		Provenance: provenance.Clone(d.triggerProvenance),
	}, d.triggerProvenance)
	if notice := selfInfluenceNotice(d.selfInfluence, d.gradientDepth, d.truncated); notice != "" {
		d.frame = notice + "\n" + d.frame
	}
	return d
}

// recordWatchSend persists a fired send as pending and returns its state.
// ok=false means the send was superseded or unresolvable (already handled).
// A persist failure after a durable prefix landed (persisted=true) still
// returns ok=true with the persisted state, so the journal and the runtime
// map can be reconciled against it instead of a ghost ok=false.
// Pure observation: no delivery, no Session calls (spec §3).
func (jm *jobManager) recordWatchSend(d watchSendDelivery, terminalEvents ...[]jobstore.Event) (state jobstore.WatchSendState, cfg *watchConfig, ok bool, err error) {
	if d.cfg == nil || d.send == nil || !jm.isCurrentWatchSendDelivery(d) {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	target, terr := resolveWatchSendTarget(d.send.To, d.watchedIdentity)
	notificationJobID := ""
	if jobID, delegateID, ok := watchFrameJob(d.eventData); ok {
		d.frame = appendWatchFrameJobRead(d.frame, jobID)
		notificationJobID = jobID
		_ = delegateID
	}
	state = jm.watchSendState(d, target)
	state.NotificationJobID = notificationJobID
	// Coalescing (recordWatchSendPending) unions the superseded pending's
	// provenance into the survivor and recomputes the self-influence depth on
	// the union — two below-threshold branches with disjoint delivered priors
	// can cross the runaway threshold only together. Everything downstream
	// (the fuse decision and any drop event) uses the PERSISTED coalesced
	// state so the recorded evidence carries the ancestry that tripped it.
	persistedState, persisted, perr := jm.persistPendingWatchSend(state, d, terminalEvents...)
	if perr != nil {
		if d.allowAfterTerminalExpiry && !persisted {
			jm.rememberUnpersistedTerminalPendingWatchSend(d.cfg, state)
		}
		if !persisted {
			return jobstore.WatchSendState{}, nil, false, perr
		}
		// The pending event (and any durable prefix of its co-generated
		// group) is already journaled and committed to the runtime map:
		// surface the persisted state with ok=true alongside the failure
		// instead of a ghost ok=false that hides the durable prefix.
		return persistedState, d.cfg, true, perr
	}
	if !persisted {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	state = persistedState
	if terr != nil {
		return state, d.cfg, false, jm.dropWatchSend(state, d.cfg, terr.Error())
	}
	if state.SelfInfluenceDepth >= runawaySelfInfluenceDepth {
		return state, d.cfg, false, jm.dropWatchSend(state, d.cfg, "runaway")
	}
	return state, d.cfg, true, nil
}

// recordWatchSendsAndKick is the observation-side half of watch delivery:
// persist every fired send, enqueue caller wake tokens, kick the owner. It
// never delivers (spec §3); the owner's loop drains and delivers on wake. With
// nothing recorded there is nothing to drain, so the owner is left undisturbed.
func (jm *jobManager) recordWatchSendsAndKick(deliveries []watchSendDelivery) {
	tokens, recorded := jm.recordWatchSends(deliveries)
	// A queued token wakes the owner by itself; a kick is what covers a
	// recorded send that queued nothing.
	if queued := jm.enqueueNotifications(tokens); recorded && !queued {
		jm.kick()
	}
}

// recordWatchSends is the durable half of recordWatchSendsAndKick: it persists
// every fired send and RETURNS the caller wake tokens rather than queueing
// them, so a caller with more to say about the same event can wake the owner
// once for all of it. recorded reports whether anything was persisted at all —
// with tokens empty, that is what still owes the owner a kick.
func (jm *jobManager) recordWatchSends(deliveries []watchSendDelivery) (tokens []jobNotification, recorded bool) {
	if len(deliveries) == 0 {
		return nil, false
	}
	deliveries = jm.snapshotWatchSendFrames(deliveries)
	for _, d := range deliveries {
		state, _, ok, _ := jm.recordWatchSend(d)
		if !ok {
			continue // recordWatchSend already produced diagnostics/drops
		}
		// A partial-persist failure still returns ok=true with the persisted
		// state: the pending frame is already journaled and committed to the
		// runtime map, so it owes the owner a wake like any recorded send —
		// otherwise the frame stalls until unrelated activity. The failure
		// itself is already queued as a diagnostic at the persist site, so it
		// coexists with the token/kick below.
		recorded = true
		if state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			tokens = append(tokens, watchSendTokenNotification("", state))
		}
	}
	return tokens, recorded
}

func (jm *jobManager) watchSendState(d watchSendDelivery, resolvedSendTo string) jobstore.WatchSendState {
	deliveryID := d.deliveryID
	if deliveryID == "" {
		deliveryID = jobstore.NewWatchSendDeliveryID()
	}
	return jobstore.WatchSendState{
		Key: jobstore.WatchSendKey{
			VisibleSessionID:        d.visibleSessionID,
			WatchID:                 d.cfg.watchID,
			WatchTarget:             d.watchTarget,
			ResolvedWatchedIdentity: d.watchedIdentity,
			ResolvedSendTo:          resolvedSendTo,
			WatchGeneration:         d.generation,
		},
		DeliveryID:               deliveryID,
		UpdateSeq:                d.updateSeq,
		Message:                  d.message,
		Frame:                    d.frame,
		TriggerIdentity:          d.watchedIdentity,
		TriggerReason:            d.trigger,
		Provenance:               provenance.Clone(d.provenance),
		ReceiverSessionID:        d.cfg.receiverSessionID,
		ReceiverDelegateID:       d.cfg.receiverDelegateID,
		SourceDelegateID:         d.cfg.sourceDelegateID,
		SourceDelegateGeneration: d.cfg.sourceGeneration,
		StableReceiver:           d.cfg.stableReceiver,
		SelfInfluenceDepth:       d.fuseDepth,
		EndNotice:                d.endNotice,
	}
}

// watchTargetSessionID resolves a delegate target to its child session.
func watchFrameJob(data events.EventData) (jobID, delegateID string, ok bool) {
	switch d := data.(type) {
	case events.JobFinishedData:
		return d.JobID, d.DelegateID, d.JobID != ""
	default:
		return "", "", false
	}
}

// jobFinishedEventData extracts the EventJobFinished payload off a session
// event's data, or ok=false for any other event kind.
func jobFinishedEventData(data events.EventData) (events.JobFinishedData, bool) {
	d, ok := data.(events.JobFinishedData)
	return d, ok
}

func (jm *jobManager) deliverPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState, ensurePending bool) (bool, error) {
	if !jm.claimWatchSendDelivery(state.DeliveryID) {
		return false, nil
	}
	defer jm.releaseWatchSendDeliveryClaim(state.DeliveryID)
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return false, nil
	}
	if ensurePending {
		if err := jm.appendWatchSendPendingState(cfg, state); err != nil {
			jm.enqueueWatchNotifications([]jobNotification{
				watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return false, err
		}
	}
	if state.StableReceiver {
		return jm.deliverStableWatchSend(cfg, state)
	}
	return false, jm.dropWatchSend(state, cfg, "retired loose watch receiver")
}

func (jm *jobManager) dropWatchSend(state jobstore.WatchSendState, cfg *watchConfig, reason string) error {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return nil
	}
	dropped := state
	dropped.DiagnosticReason = limitWatchText(reason, watchReadErrorMaxChars)
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDropped,
		TS:        jm.now(),
		WatchSend: &dropped,
	}}); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send dropped state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return err
	}
	jm.removePendingWatchSend(cfg, dropped.Key, dropped.UpdateSeq)
	jm.releaseStableWatchReceipt(dropped.DeliveryID)
	jm.enqueueWatchNotifications([]jobNotification{
		watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send failed: delivery_id="+state.DeliveryID+": "+dropped.DiagnosticReason),
	})
	return nil
}

func (jm *jobManager) deliverStableWatchSend(cfg *watchConfig, state jobstore.WatchSendState) (bool, error) {
	controller := jm.delegateController
	if controller == nil {
		return false, errors.New("stable watch controller is unavailable")
	}
	receipt := jm.stableWatchReceipt(state.DeliveryID)
	acquiredReceipt := false
	if receipt == nil {
		var err error
		receipt, err = controller.AcquireWatchDelivery(
			state.SourceDelegateID,
			state.SourceDelegateGeneration,
			state.ReceiverDelegateID,
			state.DeliveryID,
			state.UpdateSeq,
			state.EndNotice,
		)
		if err != nil {
			return false, err
		}
		jm.rememberStableWatchReceipt(receipt)
		acquiredReceipt = true
	}
	folded, err := jm.store.LoadWatchSends()
	if err != nil {
		return false, err
	}
	pending := folded.Pending[state.Key]
	if pending == nil || pending.DeliveryID != state.DeliveryID || pending.UpdateSeq != state.UpdateSeq {
		// A concurrent deliverer may have already settled this exact delivery
		// (its Delivered marker folds the old pending away). Stand down rather
		// than mistake a completed handoff for stale state, which the drain
		// path would escalate to exit 1 (#327). Reserve the hard error for
		// genuine staleness.
		if jm.watchSendDelivered(state.DeliveryID) {
			if acquiredReceipt {
				jm.releaseStableWatchReceipt(state.DeliveryID)
			}
			return false, nil
		}
		// A newer frame for the same key coalesced over this one while it was
		// being delivered, and carries its content forward: stand down. The
		// mark covers a replacement that has since settled (no pending frame
		// left); the newer durable frame covers the window between the
		// replacement's append and its runtime commit, which sets the mark.
		// Releasing the claim releases this frame's receipt once marked; a
		// commit that finds the claim already gone releases it itself.
		if jm.watchSendSuperseded(state.DeliveryID) || (pending != nil && pending.UpdateSeq > state.UpdateSeq) {
			return false, nil
		}
		return false, errors.New("stable watch delivery is not the durable pending head")
	}
	receiver, err := controller.stableWatchReceiver(state.ReceiverSessionID, state.ReceiverDelegateID)
	if err != nil {
		return false, err
	}
	attentionID := stableWatchAttentionID(state)
	_, appendErr := receiver.appendDelegateNotificationDurably(attentionID, stableWatchNotificationContent(state))
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		if appendErr != nil {
			// A frame the receiver took on an earlier attempt keeps its
			// receipt: releasing the claim hands it to the settlement retry
			// (releaseWatchSendDeliveryClaim). One never taken is done.
			if !jm.hasUnsettledWatchSend(state.DeliveryID) {
				jm.releaseStableWatchReceipt(state.DeliveryID)
			}
			return false, appendErr
		}
		if err := jm.armAcceptedStableWatchFrame(controller, receiver, cfg, state, attentionID); err != nil {
			return false, err
		}
		if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
			jm.rememberStableWatchSettlementRetry(cfg, state)
			return false, err
		}
		return false, nil
	}
	if appendErr != nil {
		return false, appendErr
	}
	if err := jm.armAcceptedStableWatchFrame(controller, receiver, cfg, state, attentionID); err != nil {
		return false, err
	}
	if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
		// While the frame stays pending, the next drain redelivers it. If a
		// replacement coalesces over it, it won't be, and the note sends it
		// to the settlement retry instead (storeWatchSendPendingRecord,
		// releaseWatchSendDeliveryClaim).
		jm.noteUnsettledWatchSend(cfg, state, false)
		return false, err
	}
	return true, nil
}

func (jm *jobManager) hasUnsettledWatchSend(deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, accepted := jm.unsettledWatchSends[deliveryID]
	return accepted
}

func (jm *jobManager) noteUnsettledWatchSend(cfg *watchConfig, state jobstore.WatchSendState, unarmed bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	// A frame whose receipt is gone was retired meanwhile (its watch cleared
	// or the frame dropped); nothing would ever clear a note for it.
	if _, held := jm.stableWatchReceipts[state.DeliveryID]; !held {
		return
	}
	if jm.unsettledWatchSends == nil {
		jm.unsettledWatchSends = make(map[string]pendingWatchSendDelivery)
	}
	jm.unsettledWatchSends[state.DeliveryID] = pendingWatchSendDelivery{cfg: cfg, state: state, unarmed: unarmed}
}

// armAcceptedStableWatchFrame arms the attention of a frame the receiver has
// durably taken. A failed arm notes the frame like a failed acknowledgement:
// while it stays pending a redelivery re-arms it, and once a replacement
// coalesces over it the settlement retry arms and acknowledges it.
func (jm *jobManager) armAcceptedStableWatchFrame(controller *delegateTreeController, receiver delegateDeliveryReceiver, cfg *watchConfig, state jobstore.WatchSendState, attentionID string) error {
	err := jm.armStableWatchFrame(controller, receiver, state.ReceiverDelegateID, attentionID)
	if err != nil {
		jm.noteUnsettledWatchSend(cfg, state, true)
	}
	return err
}

// armStableWatchFrame arms a stable watch frame's attention on its receiver,
// failing as stableWatchArmFailure says when a test sets it.
func (jm *jobManager) armStableWatchFrame(controller *delegateTreeController, receiver delegateDeliveryReceiver, receiverDelegateID, attentionID string) error {
	if jm.stableWatchArmFailure != nil {
		if err := jm.stableWatchArmFailure(attentionID); err != nil {
			return err
		}
	}
	return armStableWatchAttention(controller, receiver, receiverDelegateID, attentionID)
}

func armStableWatchAttention(controller *delegateTreeController, receiver delegateDeliveryReceiver, receiverDelegateID, attentionID string) error {
	switch receiver := receiver.(type) {
	case *Session:
		return receiver.armDelegateAttention(attentionID)
	case coldDelegateDeliveryReceiver:
		return controller.armColdDelegateAttention(receiverDelegateID, attentionID)
	}
	return nil
}

func stableWatchAttentionID(state jobstore.WatchSendState) string {
	return "watch:" + state.DeliveryID + ":" + strconv.FormatUint(state.UpdateSeq, 10)
}

func stableWatchNotificationContent(state jobstore.WatchSendState) string {
	lines := strings.Split(state.Frame, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "delivery_id:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (jm *jobManager) isCurrentPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.isCurrentPendingWatchSendLocked(cfg, state)
}

func (jm *jobManager) isCurrentPendingWatchSendLocked(cfg *watchConfig, state jobstore.WatchSendState) bool {
	terminalFlush := cfg != nil && jm.terminalFlush != nil && jm.terminalFlush[cfg]
	if cfg == nil || jm.closing || (cfg.rejectingDelivery && !terminalFlush) {
		return false
	}
	pending := cfg.pending[state.Key]
	if pending == nil || pending.UpdateSeq != state.UpdateSeq || pending.DeliveryID != state.DeliveryID {
		return false
	}
	if terminalFlush {
		return true
	}
	key := watchKey{VisibleSessionID: state.Key.VisibleSessionID, Target: state.Key.WatchTarget, SendTo: cfg.sendTo(), ReceiverSessionID: cfg.receiverSessionID, ReceiverDelegateID: cfg.receiverDelegateID}
	return jm.watches[key] == cfg
}

func (cfg *watchConfig) sendTo() string {
	if cfg == nil || cfg.send == nil {
		return ""
	}
	return cfg.send.To
}

func (jm *jobManager) rememberUnpersistedTerminalPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if cfg == nil || cfg.rejectingDelivery || jm.closing {
		return
	}
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	if cfg.pending[state.Key] == nil {
		cfg.pendingOrder = append(cfg.pendingOrder, state.Key)
	}
	now := jm.now()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	pending := state
	cfg.pending[state.Key] = &pending
	jm.rememberDetachedPendingLocked(cfg)
}

func (jm *jobManager) isCurrentWatchSendDelivery(d watchSendDelivery) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.isCurrentWatchSendDeliveryLocked(d)
}

func (jm *jobManager) isCurrentWatchSendDeliveryLocked(d watchSendDelivery) bool {
	if d.cfg == nil || d.cfg.generation != d.generation || (d.cfg.rejectingDelivery && !d.allowAfterTerminalExpiry) || jm.closing {
		return false
	}
	if d.allowAfterTerminalExpiry {
		return jm.terminalFlush != nil && jm.terminalFlush[d.cfg]
	}
	return jm.watches[d.key] == d.cfg
}

func (jm *jobManager) persistPendingWatchSend(state jobstore.WatchSendState, d watchSendDelivery, terminalEvents ...[]jobstore.Event) (jobstore.WatchSendState, bool, error) {
	releasePersistence := jm.beginWatchPersistence()
	defer releasePersistence()

	record := jm.planWatchSendPending(state, d)
	if len(record.pendingEvents) == 0 {
		return state, false, nil
	}
	var teardownEvents []jobstore.Event
	if len(terminalEvents) != 0 {
		teardownEvents = terminalEvents[0]
	}
	enqueueReceipt, err := jm.beginStableWatchEnqueue(record.persisted)
	if err != nil {
		return state, false, err
	}
	enqueueCompleted := false
	defer func() {
		if enqueueReceipt != nil && !enqueueCompleted {
			jm.observeWatchReceiptBoundary()
			enqueueReceipt.controller.AbortWatchEnqueue(enqueueReceipt)
		}
	}()
	// Persist the pending event and any cap-overflow eviction terminal events
	// as one group: AppendBatch writes them with a single fsync and rolls the
	// whole group back on failure (all-or-nothing). A lone pending event stays
	// on the appendEvent seam inside appendWatchSendEvents.
	// Without the batch seam (jm.appendEvents == nil) a multi-event group has
	// no atomic write: keep the pre-batch sequential protocol instead, so a
	// later eviction write can fail while the pending event is already
	// durable — persisted=true with each durable prefix committed to the
	// runtime map — instead of leaving a durable prefix behind an ok=false
	// return that never commits it.
	if len(record.evictions) != 0 && jm.appendEvents == nil && len(teardownEvents) == 0 {
		if err := jm.appendWatchSendEvents(record.pendingEvents); err != nil {
			jm.enqueueWatchNotifications([]jobNotification{
				watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return state, false, err
		}
		// Promote the receipt before the runtime map publishes the frame to
		// drains, so a drain that delivers it at once reuses that receipt.
		completed, verr := jm.verifyStableWatchEnqueue(enqueueReceipt, record.persisted)
		enqueueCompleted = completed
		jm.commitWatchSendPendingRecord(record, d.allowAfterTerminalExpiry)
		var evictionDiagnostics []jobNotification
		for _, eviction := range record.evictions {
			applied, err := jm.appendWatchSendTerminalSnapshots([]watchSendTerminalSnapshot{eviction.terminal})
			if err != nil {
				jm.removeWatchSendTerminalSnapshots(applied)
				jm.enqueueWatchNotifications([]jobNotification{
					watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
				})
				return record.persisted, true, err
			}
			jm.removeWatchSendTerminalSnapshots(applied)
			evictionDiagnostics = append(evictionDiagnostics, eviction.diagnostic)
		}
		for _, diagnostic := range evictionDiagnostics {
			jm.enqueueWatchNotifications([]jobNotification{diagnostic})
		}
		return record.persisted, true, verr
	}
	group := append([]jobstore.Event(nil), record.pendingEvents...)
	group = append(group, teardownEvents...)
	var evictionSnapshots []watchSendTerminalSnapshot
	for _, eviction := range record.evictions {
		evictionSnapshots = append(evictionSnapshots, eviction.terminal)
		group = append(group, eviction.terminal.events...)
	}
	if err := jm.appendWatchSendEvents(group); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return state, false, err
	}
	// The eviction events landed with the pending event above: drop the
	// evicted keys from the runtime map (releasing their receipts) first, so
	// no drain woken during verification finds a frame that is no longer
	// durably pending.
	jm.removeWatchSendTerminalSnapshots(evictionSnapshots)
	// Promote the receipt and check the refold before the runtime map
	// publishes the frame to drains: a drain that delivers it at once then
	// reuses that receipt, and its Delivered marker can't fail the refold.
	completed, verr := jm.verifyStableWatchEnqueue(enqueueReceipt, record.persisted)
	enqueueCompleted = completed
	jm.commitWatchSendPendingRecord(record, d.allowAfterTerminalExpiry)
	var evictionDiagnostics []jobNotification
	for _, eviction := range record.evictions {
		evictionDiagnostics = append(evictionDiagnostics, eviction.diagnostic)
	}
	for _, diagnostic := range evictionDiagnostics {
		jm.enqueueWatchNotifications([]jobNotification{diagnostic})
	}
	return record.persisted, true, verr
}

// verifyStableWatchEnqueue completes an admitted stable enqueue and checks the
// persisted pending frame survives a durable refold. Callers run it before
// committing the frame to the runtime map, so no drain sees the frame before
// its receipt is promoted and remembered. Both persist branches
// (the nil-seam sequential protocol and the batch group write) share it so
// their verification cannot diverge. completed reports whether
// CompleteWatchEnqueue consumed the receipt: the caller must still skip the
// deferred abort when a later refold check fails. A nil receipt is a
// non-stable send: nothing to verify.
func (jm *jobManager) verifyStableWatchEnqueue(enqueueReceipt *delegateWatchReceipt, persisted jobstore.WatchSendState) (completed bool, err error) {
	if enqueueReceipt == nil {
		return false, nil
	}
	jm.observeWatchReceiptBoundary()
	deliveryReceipt, err := enqueueReceipt.controller.CompleteWatchEnqueue(enqueueReceipt)
	if err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification(persisted.Key.WatchID, persisted.Key.ResolvedWatchedIdentity, "watch send stable enqueue failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return false, err
	}
	jm.rememberStableWatchReceipt(deliveryReceipt)
	folded, err := jm.store.LoadWatchSends()
	if err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification(persisted.Key.WatchID, persisted.Key.ResolvedWatchedIdentity, "watch send stable enqueue failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return true, err
	}
	pending := folded.Pending[persisted.Key]
	if pending == nil || pending.DeliveryID != persisted.DeliveryID || pending.UpdateSeq != persisted.UpdateSeq {
		verr := errors.New("stable watch pending frame did not survive durable refold")
		jm.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification(persisted.Key.WatchID, persisted.Key.ResolvedWatchedIdentity, "watch send stable enqueue failed: "+limitWatchText(verr.Error(), watchReadErrorMaxChars)),
		})
		return true, verr
	}
	return true, nil
}

func (jm *jobManager) beginWatchPersistence() func() {
	for {
		jm.watchPersistMu.Lock()
		if jm.watchPersistDone == nil {
			done := make(chan struct{})
			jm.watchPersistDone = done
			jm.watchPersistMu.Unlock()
			return func() {
				jm.watchPersistMu.Lock()
				jm.watchPersistDone = nil
				close(done)
				jm.watchPersistMu.Unlock()
			}
		}
		done := jm.watchPersistDone
		jm.watchPersistMu.Unlock()
		<-done
	}
}

func (jm *jobManager) beginStableWatchEnqueue(state jobstore.WatchSendState) (*delegateWatchReceipt, error) {
	if !state.StableReceiver {
		return nil, nil
	}
	if jm.delegateController == nil {
		return nil, errors.New("stable watch controller is unavailable")
	}
	jm.observeWatchReceiptBoundary()
	return jm.delegateController.BeginWatchEnqueue(
		state.SourceDelegateID,
		state.SourceDelegateGeneration,
		state.ReceiverDelegateID,
		state.DeliveryID,
		state.UpdateSeq,
		state.EndNotice,
	)
}

type watchSendPendingRecord struct {
	pendingEvents []jobstore.Event
	evictions     []watchSendEviction
	cfg           *watchConfig
	key           jobstore.WatchSendKey
	previous      *jobstore.WatchSendState
	// persisted is the exact state written to the pending map and event log
	// (post-coalescing: unioned provenance, recomputed self-influence depth).
	persisted jobstore.WatchSendState
}

type watchSendEviction struct {
	terminal   watchSendTerminalSnapshot
	diagnostic jobNotification
}

func (jm *jobManager) planWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) watchSendPendingRecord {
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if !jm.isCurrentWatchSendDeliveryLocked(d) {
		return watchSendPendingRecord{}
	}
	cfg := d.cfg
	if settled, ok := cfg.settledUpdateSeq[state.Key]; ok && state.UpdateSeq <= settled {
		return watchSendPendingRecord{}
	}
	// The pending map is created only after the pending event is durable.
	record := watchSendPendingRecord{
		cfg: cfg,
		key: state.Key,
	}
	if existing := cfg.pending[state.Key]; existing != nil {
		if state.UpdateSeq < existing.UpdateSeq {
			return watchSendPendingRecord{}
		}
		previous := *existing
		record.previous = &previous
		state.CoalescedCount = existing.CoalescedCount + 1
		state.CreatedAt = existing.CreatedAt
		// The coalesced frame stands in for both the superseded delivery and this
		// one, so it must carry both causes so either's downstream echo is
		// recognized — and the breaker must judge the UNION: two below-threshold
		// branches with disjoint delivered priors can cross the runaway
		// threshold only together.
		state.Provenance = provenance.Union(existing.Provenance, state.Provenance)
		if ud := jm.fuseDepthLocked(cfg, state.Provenance); ud > state.SelfInfluenceDepth {
			state.SelfInfluenceDepth = ud
		}
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	pendingState := state
	record.persisted = pendingState

	record.pendingEvents = []jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        now,
		WatchSend: &pendingState,
	}}
	projectedPending := len(cfg.pending)
	if cfg.pending[state.Key] == nil {
		projectedPending++
	}
	overflow := projectedPending - defaultWatchSendPendingCap
	for _, evictedKey := range cfg.pendingOrder {
		if overflow <= 0 {
			break
		}
		evicted := cfg.pending[evictedKey]
		if evicted == nil {
			continue
		}
		evictedState := *evicted
		evictedState.DiagnosticReason = "pending cap exceeded"
		record.evictions = append(record.evictions, watchSendEviction{
			terminal: watchSendTerminalSnapshot{cfg: cfg, events: []jobstore.Event{{
				Kind:      jobstore.EventWatchSendEvicted,
				TS:        now,
				WatchSend: &evictedState,
			}}},
			diagnostic: watchSendDiagnosticNotification(evictedState.Key.WatchID, evictedState.Key.ResolvedWatchedIdentity, "watch send evicted: "+evictedState.TriggerIdentity),
		})
		overflow--
	}
	return record
}

// commitWatchSendPendingRecord installs record's frame as its key's pending
// frame. A frame it replaces under coalescing had its own delivery id, which
// nothing will deliver, drop or settle again, so the replaced frame's receipt
// is released here; otherwise it would fence the receiver's stop until the
// close budget ran out.
func (jm *jobManager) commitWatchSendPendingRecord(record watchSendPendingRecord, detached bool) {
	if release := jm.storeWatchSendPendingRecord(record, detached); release != "" {
		jm.releaseStableWatchReceipt(release)
	}
}

// storeWatchSendPendingRecord installs record's frame as its key's pending
// frame and returns the delivery id of the replaced frame whose receipt the
// caller releases, or "" when there is none to release now. A replaced frame
// a deliverer holds the claim on is marked superseded under the same hold that
// publishes its replacement, so that deliverer sees the mark however soon the
// replacement settles; its receipt goes with the claim
// (releaseWatchSendDeliveryClaim). An unclaimed replaced frame the receiver
// took without an acknowledgement goes to the settlement retry, which keeps
// its receipt; the persist caller's wake of the owner runs it
// (recordWatchSendsAndKick).
func (jm *jobManager) storeWatchSendPendingRecord(record watchSendPendingRecord, detached bool) (release string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if record.cfg == nil || len(record.pendingEvents) == 0 {
		return ""
	}
	cfg := record.cfg
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	current := cfg.pending[record.key]
	if current != nil && current.UpdateSeq > record.persisted.UpdateSeq {
		return ""
	}
	if current == nil {
		cfg.pendingOrder = append(cfg.pendingOrder, record.key)
	} else if replaced := current.DeliveryID; replaced != "" && replaced != record.persisted.DeliveryID {
		if _, delivering := jm.deliveringWatchSends[replaced]; delivering {
			if jm.supersededWatchSends == nil {
				jm.supersededWatchSends = make(map[string]struct{})
			}
			jm.supersededWatchSends[replaced] = struct{}{}
		} else if !jm.retryUnsettledWatchSendLocked(replaced) {
			release = replaced
		}
	}
	persisted := record.persisted
	cfg.pending[record.key] = &persisted
	if detached || cfg.rejectingDelivery || jm.closing {
		jm.rememberDetachedPendingLocked(cfg)
	}
	return release
}

func (jm *jobManager) removePendingWatchSend(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	removePendingWatchSendLocked(cfg, key, updateSeq)
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
}

func (jm *jobManager) removeRuntimePendingWatchSend(state jobstore.WatchSendState) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg := jm.watchConfigForKeyLocked(state.Key)
	if cfg == nil {
		return
	}
	removePendingWatchSendLocked(cfg, state.Key, state.UpdateSeq)
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
}

func removePendingWatchSendLocked(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	if cfg == nil {
		return
	}
	settleWatchSendLocked(cfg, key, updateSeq)
	if cfg.pending == nil {
		return
	}
	pending, ok := cfg.pending[key]
	if !ok || updateSeq < pending.UpdateSeq {
		return
	}
	deletePendingWatchSendKeyLocked(cfg, key)
}

func deletePendingWatchSendKeyLocked(cfg *watchConfig, key jobstore.WatchSendKey) {
	if cfg == nil || cfg.pending == nil {
		return
	}
	delete(cfg.pending, key)
	for i, orderedKey := range cfg.pendingOrder {
		if orderedKey != key {
			continue
		}
		copy(cfg.pendingOrder[i:], cfg.pendingOrder[i+1:])
		cfg.pendingOrder = cfg.pendingOrder[:len(cfg.pendingOrder)-1]
		return
	}
}

func settleWatchSendLocked(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	if cfg.settledUpdateSeq == nil {
		cfg.settledUpdateSeq = make(map[jobstore.WatchSendKey]uint64)
	}
	if _, ok := cfg.settledUpdateSeq[key]; !ok {
		cfg.settledOrder = append(cfg.settledOrder, key)
	}
	if updateSeq > cfg.settledUpdateSeq[key] {
		cfg.settledUpdateSeq[key] = updateSeq
	}
	for len(cfg.settledOrder) > defaultWatchSendPendingCap {
		oldest := cfg.settledOrder[0]
		copy(cfg.settledOrder, cfg.settledOrder[1:])
		cfg.settledOrder = cfg.settledOrder[:len(cfg.settledOrder)-1]
		delete(cfg.settledUpdateSeq, oldest)
	}
}

func (jm *jobManager) forgetTerminalFlushIfEmptyLocked(cfg *watchConfig) {
	if cfg == nil || cfg.terminalDeliveryPending || len(cfg.pending) != 0 || jm.terminalFlush == nil {
		return
	}
	delete(jm.terminalFlush, cfg)
}

func (jm *jobManager) rememberDetachedPendingLocked(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
}

type watchSendTerminalSnapshot struct {
	cfg    *watchConfig
	events []jobstore.Event
}

type watchConfigTerminalSnapshot struct {
	key      watchKey
	cfg      *watchConfig
	terminal watchSendTerminalSnapshot
	// endReason, when set, is recorded to the watch history ring as the config is
	// detached from the active set (cleared / budget_exhausted). Left empty by
	// snapshot callers that are not removing an active watch.
	endReason string
}

func terminalSnapshots(targets []watchConfigTerminalSnapshot) []watchSendTerminalSnapshot {
	out := make([]watchSendTerminalSnapshot, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.terminal)
	}
	return out
}

func markWatchConfigSnapshotsRejectingLocked(targets []watchConfigTerminalSnapshot) {
	for _, target := range targets {
		if target.cfg != nil {
			target.cfg.rejectingDelivery = true
		}
	}
}

func (jm *jobManager) rollbackWatchConfigSnapshotsRejecting(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchConfigSnapshotsRejectingLocked(jm, targets)
}

func rollbackWatchConfigSnapshotsRejectingLocked(jm *jobManager, targets []watchConfigTerminalSnapshot) {
	for _, target := range targets {
		if target.cfg != nil && jm.watches[target.key] == target.cfg {
			target.cfg.rejectingDelivery = false
		}
	}
}

// watchTeardown is the set of configs a teardown freezes while its end is
// persisted: the live watches being ended and the detached configs whose
// pending frames are being dropped. Each site keeps its own persist call and
// failure policy; the mark and the rollback are what they share.
type watchTeardown struct {
	targets      []watchConfigTerminalSnapshot
	detachedCfgs []*watchConfig
}

// markRejectingLocked freezes delivery on every config in the teardown. The
// caller holds jm.mu.
func (t watchTeardown) markRejectingLocked() {
	markWatchConfigSnapshotsRejectingLocked(t.targets)
	markWatchConfigsRejectingLocked(t.detachedCfgs)
}

// rollbackLocked re-enables delivery on the configs that are still where the
// teardown found them. The caller holds jm.mu.
func (t watchTeardown) rollbackLocked(jm *jobManager) {
	rollbackWatchConfigSnapshotsRejectingLocked(jm, t.targets)
	rollbackWatchConfigsRejectingLocked(jm, t.detachedCfgs)
}

func (t watchTeardown) rollback(jm *jobManager) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	t.rollbackLocked(jm)
}

func (jm *jobManager) closeWatchConfigSnapshots(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, target := range targets {
		closeWatchConfig(target.cfg)
	}
}

func (jm *jobManager) detachWatchConfigSnapshots(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, target := range targets {
		jm.detachWatchConfigSnapshotLocked(target)
	}
}

// detachWatchConfigSnapshotLocked removes one target's config from the live set
// and reports whether THIS call was the one that removed it. False means the key
// no longer held that config — a concurrent teardown already took it, and owns
// whatever happens to it next. The caller must hold jm.mu.
func (jm *jobManager) detachWatchConfigSnapshotLocked(target watchConfigTerminalSnapshot) bool {
	if target.cfg == nil || jm.watches[target.key] != target.cfg {
		return false
	}
	if target.endReason != "" {
		jm.recordWatchEndedLocked(target.key, target.cfg, target.endReason)
	}
	closeWatchConfig(target.cfg)
	delete(jm.watches, target.key)
	return true
}

func watchSendTerminalSnapshotsLocked(cfg *watchConfig, kind jobstore.EventKind, reason string, now time.Time) watchSendTerminalSnapshot {
	snapshot := watchSendTerminalSnapshot{cfg: cfg}
	if cfg == nil || len(cfg.pending) == 0 {
		return snapshot
	}
	snapshot.events = make([]jobstore.Event, 0, len(cfg.pending))
	for _, key := range cfg.pendingOrder {
		state := cfg.pending[key]
		if state == nil {
			continue
		}
		terminal := *state
		terminal.DiagnosticReason = reason
		snapshot.events = append(snapshot.events, jobstore.Event{
			Kind:      kind,
			TS:        now,
			WatchSend: &terminal,
		})
	}
	return snapshot
}

func (jm *jobManager) detachedWatchSendTerminalSnapshotsLocked(key watchKey, kind jobstore.EventKind, reason string, now time.Time) ([]*watchConfig, []watchSendTerminalSnapshot) {
	var cfgs []*watchConfig
	var snapshots []watchSendTerminalSnapshot
	for cfg := range jm.terminalFlush {
		if !watchConfigMatchesWatchKey(cfg, key) {
			continue
		}
		snapshot := watchSendTerminalSnapshotMatchingKeyLocked(cfg, key, kind, reason, now)
		if len(snapshot.events) != 0 {
			cfgs = append(cfgs, cfg)
			snapshots = append(snapshots, snapshot)
			continue
		}
		if watchConfigSendToMatchesWatchKey(cfg, key) {
			cfgs = append(cfgs, cfg)
		}
	}
	return cfgs, snapshots
}

func (jm *jobManager) detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID string, kind jobstore.EventKind, reason string, now time.Time, allow func(*watchConfig) bool) ([]*watchConfig, []watchSendTerminalSnapshot, bool) {
	if watchID == "" {
		return nil, nil, false
	}
	var cfgs []*watchConfig
	var snapshots []watchSendTerminalSnapshot
	hidden := false
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID != watchID {
			continue
		}
		if allow != nil && !allow(cfg) {
			hidden = true
			continue
		}
		snapshot := watchSendTerminalSnapshotsLocked(cfg, kind, reason, now)
		cfgs = append(cfgs, cfg)
		if len(snapshot.events) != 0 {
			snapshots = append(snapshots, snapshot)
		}
	}
	return cfgs, snapshots, hidden
}

func watchSendTerminalSnapshotMatchingKeyLocked(cfg *watchConfig, key watchKey, kind jobstore.EventKind, reason string, now time.Time) watchSendTerminalSnapshot {
	snapshot := watchSendTerminalSnapshot{cfg: cfg}
	if cfg == nil || len(cfg.pending) == 0 {
		return snapshot
	}
	for _, pendingKey := range cfg.pendingOrder {
		if !watchSendKeyMatchesWatchKey(pendingKey, key) {
			continue
		}
		state := cfg.pending[pendingKey]
		if state == nil {
			continue
		}
		terminal := *state
		terminal.DiagnosticReason = reason
		snapshot.events = append(snapshot.events, jobstore.Event{
			Kind:      kind,
			TS:        now,
			WatchSend: &terminal,
		})
	}
	return snapshot
}

func watchSendKeyMatchesWatchKey(pending jobstore.WatchSendKey, key watchKey) bool {
	// A durable send key carries no slot, and only send-rail configs reach
	// here, so a timer key never matches a pending send.
	if key.Slot != "" {
		return false
	}
	if pending.VisibleSessionID != key.VisibleSessionID || pending.WatchTarget != key.Target {
		return false
	}
	if key.SendTo == "" || pending.ResolvedSendTo == key.SendTo {
		return true
	}
	return key.SendTo == runtimeMessageAliasWatched &&
		pending.ResolvedWatchedIdentity != "" &&
		pending.ResolvedSendTo == pending.ResolvedWatchedIdentity
}

func watchConfigMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil || cfg.target != key.Target {
		return false
	}
	if cfg.slot != key.Slot {
		return false
	}
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	if key.SendTo == "" {
		return true
	}
	if key.SendTo == runtimeMessageAliasWatched {
		return true
	}
	return watchConfigSendToMatchesWatchKey(cfg, key)
}

func watchConfigSendToMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	if key.SendTo == "" {
		return true
	}
	return cfg.send != nil && cfg.send.To == key.SendTo
}

func watchConfigReceiverMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil {
		return false
	}
	if key.ReceiverSessionID == "" && key.ReceiverDelegateID == "" {
		return true
	}
	return cfg.receiverSessionID == key.ReceiverSessionID &&
		cfg.receiverDelegateID == key.ReceiverDelegateID
}

func markWatchConfigsRejectingLocked(cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		if cfg != nil {
			cfg.rejectingDelivery = true
		}
	}
}

func (jm *jobManager) rollbackWatchConfigsRejecting(cfgs []*watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchConfigsRejectingLocked(jm, cfgs)
}

func rollbackWatchConfigsRejectingLocked(jm *jobManager, cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		if cfg != nil && jm.terminalFlush != nil && jm.terminalFlush[cfg] {
			cfg.rejectingDelivery = false
		}
	}
}

func (jm *jobManager) forgetDetachedWatchSendConfigsIfEmpty(cfgs []*watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range cfgs {
		jm.forgetTerminalFlushIfEmptyLocked(cfg)
	}
}

func (jm *jobManager) appendWatchSendTerminalSnapshots(snapshots []watchSendTerminalSnapshot) ([]watchSendTerminalSnapshot, error) {
	applied := make([]watchSendTerminalSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		appliedSnapshot := watchSendTerminalSnapshot{cfg: snapshot.cfg}
		for _, event := range snapshot.events {
			if err := jm.appendEvent(event); err != nil {
				if len(appliedSnapshot.events) != 0 {
					applied = append(applied, appliedSnapshot)
				}
				return applied, err
			}
			appliedSnapshot.events = append(appliedSnapshot.events, event)
		}
		if len(appliedSnapshot.events) != 0 {
			applied = append(applied, appliedSnapshot)
		}
	}
	return applied, nil
}

func (jm *jobManager) removeWatchSendTerminalSnapshots(snapshots []watchSendTerminalSnapshot) {
	jm.mu.Lock()
	var receiptIDs []string
	for _, snapshot := range snapshots {
		for _, event := range snapshot.events {
			if event.WatchSend == nil {
				continue
			}
			receiptIDs = append(receiptIDs, event.WatchSend.DeliveryID)
			removePendingWatchSendLocked(snapshot.cfg, event.WatchSend.Key, event.WatchSend.UpdateSeq)
		}
		jm.forgetTerminalFlushIfEmptyLocked(snapshot.cfg)
	}
	jm.mu.Unlock()
	for _, deliveryID := range receiptIDs {
		jm.releaseStableWatchReceipt(deliveryID)
	}
}

func (jm *jobManager) appendWatchSendEvents(events []jobstore.Event) error {
	if len(events) == 0 {
		return nil
	}
	// Batch multi-event groups through AppendBatch: one fsync, all-or-nothing,
	// so a co-generated group either lands together or rolls back together.
	// Single-event appends stay on the appendEvent seam — one fsync either way,
	// and fault-injection harnesses stubbing only appendEvent keep intercepting
	// settle/drop/pending writes (same shape as appendJobEvents).
	if len(events) > 1 && jm.appendEvents != nil {
		if err := jm.appendEvents(events); err != nil {
			return err
		}
		jm.noteAcceptedWatchEvents(events)
		return nil
	}
	for _, e := range events {
		if err := jm.appendEvent(e); err != nil {
			return err
		}
		jm.noteAcceptedWatchEvents([]jobstore.Event{e})
	}
	return nil
}

func (jm *jobManager) appendWatchSendPendingState(cfg *watchConfig, state jobstore.WatchSendState) error {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return nil
	}
	pending := state
	return jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        jm.now(),
		WatchSend: &pending,
	}})
}

type pendingWatchSendDelivery struct {
	cfg   *watchConfig
	state jobstore.WatchSendState
	// unarmed marks a settlement retry for a frame the receiver took whose
	// attention arm failed: the retry arms it before acknowledging it.
	unarmed bool
}

// drainJobManagerWatchSends drains jm through the session that owns jm. The
// receiver is the control surface for delegate-targeted sends; childSessionID is
// only the caller-token routing marker used when a parent scans a child store.
func (s *Session) drainJobManagerWatchSends(ctx context.Context, jm *jobManager, childSessionID string) (watchSendDrainResult, error) {
	var result watchSendDrainResult
	var errs []error
	retrySettled, err := jm.retryStableWatchSettlements()
	if err != nil {
		return result, err
	}
	if !retrySettled {
		return result, nil
	}
	for _, delivery := range jm.pendingWatchSendDeliveries(nil) {
		target := delivery.state.Key.ResolvedSendTo
		if target == runtimeMessageAliasCaller {
			// Caller sends render on the OWNING session's own rail. Tokens are
			// enqueued at observation time; this re-token covers restored /
			// crash-recovered pendings. Duplicates are harmless (render-by-key +
			// batch dedupe). A mid-level owner's caller frame renders in the mid's
			// OWN drive turn (spec §3 mid-owner caller sends): the parent drives
			// the mid on the pending-watch-send signal, and the mid renders here
			// against its own rail (childSessionID == "" from the mid's view). The
			// v2 child-iteration re-route — re-tokening a child's caller pending
			// onto the PARENT's rail with ChildSessionID set — is deleted; the
			// drive signal replaces it, so only the owning session enqueues its
			// own caller token.
			if jm.enqueue != nil && childSessionID == "" {
				jm.enqueue(watchSendTokenNotification("", delivery.state))
			}
			continue
		}
		delivered, err := jm.deliverPendingWatchSend(delivery.cfg, delivery.state, true)
		result.observerHandoff = result.observerHandoff || delivered
		if err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

func (jm *jobManager) pendingWatchSendDeliveries(include func(*jobstore.WatchSendState) bool) []pendingWatchSendDelivery {
	jm.mu.Lock()

	var all []pendingWatchSendDelivery
	seen := make(map[*watchConfig]bool)
	collect := func(cfg *watchConfig) {
		if cfg == nil || seen[cfg] {
			return
		}
		seen[cfg] = true
		for _, key := range cfg.pendingOrder {
			state := cfg.pending[key]
			if state == nil {
				continue
			}
			copied := *state
			all = append(all, pendingWatchSendDelivery{cfg: cfg, state: copied})
		}
	}
	for _, cfg := range jm.watches {
		collect(cfg)
	}
	for cfg := range jm.terminalFlush {
		collect(cfg)
	}
	jm.mu.Unlock()

	deliveries := all[:0]
	for _, delivery := range all {
		if include == nil || include(&delivery.state) {
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries
}

func (jm *jobManager) kick() {
	if jm.wake != nil {
		jm.wake()
	}
}

// hasLiveUnfiredWatchOnTarget reports whether any ARMED watch that has not yet
// matched its condition targets jobID. Progress ticks and teardown notices are
// deliberately not condition fires: they say the watcher is alive, not that
// the model's requested condition occurred. The undisposed-background-job
// announcement reads this directly rather than inferring "watched" from watch
// frames in the notification queue: on a long progress interval the queue is
// empty between frames, and inferring from it would re-announce — and eventually
// kill — a job the model explicitly said it was waiting on. A cleared watch
// (model-cleared, or the delivery-budget auto-clear) leaves this map, so the job
// counts as undisposed again. A watch that has just tripped the budget is
// retained as a temporary excuse while its asynchronous teardown persists the
// clear and queues the final notification; otherwise the drain can announce in
// that handoff window before it receives the notification that explains the
// clear. That window is keyed on conditionFires, the counter the breaker
// latches on, so it closes when the teardown removes the config.
func (jm *jobManager) hasLiveUnfiredWatchOnTarget(jobID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if key.Target == jobID && (cfg.conditionFires == 0 || cfg.conditionFires >= watchDeliveryBudget) {
			return true
		}
	}
	return false
}

// hasPendingWatchSends reports whether any live or terminal-flush watch config
// holds undelivered pending sends. Drain-loop tails use it to decide whether a
// wake needs a drain pass.
func (jm *jobManager) hasPendingWatchSends() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying {
		return true
	}
	for _, cfg := range jm.watches {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	for cfg := range jm.terminalFlush {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	return false
}

func resolveWatchSendTarget(target, watchedJobID string) (string, error) {
	if target != runtimeMessageAliasWatched {
		return target, nil
	}
	// New watch configurations reject the watched alias. Keep resolution here
	// so historical pending sends can still be folded, drained, or cleared.
	if watchedJobID == "" || isWatchSessionTarget(watchedJobID) {
		return "", errors.New("watched_unresolved")
	}
	return watchedJobID, nil
}
