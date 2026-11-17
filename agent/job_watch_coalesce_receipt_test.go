package agent

import (
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// stableWatchDeliveryReceiptIDs lists the delivery ids the controller still
// holds a delivery receipt for. A receipt fences a delegate stop until it
// completes, so one that outlives its frame stalls closing the tree.
func stableWatchDeliveryReceiptIDs(c *delegateTreeController) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, receipt := range c.watchDeliveries {
		ids = append(ids, receipt.deliveryID)
	}
	slices.Sort(ids)
	return ids
}

// requireOnlyStableWatchReceipts fails unless the controller holds delivery
// receipts for exactly ids.
func requireOnlyStableWatchReceipts(t *testing.T, fixture *stableWatchRuntimeFixture, ids ...string) {
	t.Helper()
	want := slices.Sorted(slices.Values(ids))
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Equal(got, want) {
		t.Fatalf("delivery receipts = %v, want only %v", got, want)
	}
}

// stableWatchSettlementRetrying reports whether deliveryID is queued for the
// settlement retry.
func stableWatchSettlementRetrying(jm *jobManager, deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, retrying := jm.stableWatchSettlementRetries[deliveryID]
	return retrying
}

// requireSettlementRetryAcknowledges runs the settlement retry and requires it
// to acknowledge accepted, leaving only pending's receipt.
func requireSettlementRetryAcknowledges(t *testing.T, fixture *stableWatchRuntimeFixture, accepted, pending jobstore.WatchSendState) {
	t.Helper()
	if _, err := fixture.sourceJM.retryStableWatchSettlements(); err != nil {
		t.Fatalf("settlement retry: %v", err)
	}
	if !fixture.sourceJM.watchSendDelivered(accepted.DeliveryID) {
		t.Fatal("the retry did not acknowledge the accepted frame")
	}
	requireOnlyStableWatchReceipts(t, fixture, pending.DeliveryID)
}

// When a second frame coalesces over a stable-receiver frame that is still
// pending, the superseded frame's delivery receipt must be released with it:
// only the frame that will be delivered keeps one. Otherwise the orphaned
// receipt fences the receiver's stop forever, and closing the root waits out
// the close budget.
func TestCoalescedStableWatchFrameReleasesSupersededReceipt(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if second.DeliveryID == first.DeliveryID {
		t.Fatalf("coalesced frame kept delivery id %s", first.DeliveryID)
	}
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}

// A deliverer already handing off the first frame when the second coalesces
// over it keeps the first receipt through the supersede, then stands down
// without an error and releases it: the newer frame carries the content on.
func TestInFlightSupersededStableWatchFrameStandsDown(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); len(got) != 2 {
		t.Fatalf("the in-flight first frame lost its receipt at the supersede: %v", got)
	}
	delivered, err := fixture.sourceJM.deliverStableWatchSend(fixture.onlyWatchConfig(t), first)
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if delivered || err != nil {
		t.Fatalf("superseded deliverer: delivered=%t err=%v, want a quiet stand-down", delivered, err)
	}
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}

// A deliverer that claimed the first frame and then exits early, because the
// frame stopped being the current pending one, releases the receipt with its
// claim: the coalescing commit left the receipt to it.
func TestSupersededStableWatchReceiptReleasesWithEarlyExitClaim(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if fixture.sourceJM.isCurrentPendingWatchSend(fixture.onlyWatchConfig(t), first) {
		t.Fatal("first frame still current after the coalesce")
	}
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}

// When the replacement is delivered before the superseded deliverer reaches
// its durable check, there is no pending frame left for the key at all. The
// superseded deliverer still stands down without a staleness error, and its
// receipt goes with its claim, leaving none behind.
func TestSupersededStableWatchDelivererAfterReplacementDelivered(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if delivered, err := fixture.sourceJM.deliverPendingWatchSend(cfg, second, false); !delivered || err != nil {
		t.Fatalf("deliver replacement: delivered=%t err=%v", delivered, err)
	}
	delivered, err := fixture.sourceJM.deliverStableWatchSend(cfg, first)
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if delivered || err != nil {
		t.Fatalf("superseded deliverer: delivered=%t err=%v, want a quiet stand-down", delivered, err)
	}
	requireOnlyStableWatchReceipts(t, fixture)
}

// The replacement frame is durable before its runtime commit marks the
// claimed first frame superseded. A deliverer of the first frame that reaches
// its durable check in that window finds a newer pending frame for its key,
// stands down without a staleness error, and its receipt still goes once the
// coalesce and its claim have both finished.
func TestSupersededStableWatchDelivererBeforeRuntimeCommit(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	var standDown error
	ranInWindow := false
	originalAppend := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if err := originalAppend(event); err != nil {
			return err
		}
		if !ranInWindow && event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.DeliveryID != first.DeliveryID {
			ranInWindow = true
			var delivered bool
			delivered, standDown = jm.deliverStableWatchSend(cfg, first)
			if delivered {
				standDown = errors.New("superseded frame was delivered")
			}
		}
		return nil
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	jm.appendEvent = originalAppend
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if !ranInWindow {
		t.Fatal("the replacement's pending append never ran")
	}
	if standDown != nil {
		t.Fatalf("deliverer in the commit window: %v, want a quiet stand-down", standDown)
	}
	second := fixture.requireOnePending(t).state
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}

// watchSendFireRecorded reports whether the fire of deliveryID survives in p's
// fire record or its diagnostic chain.
func watchSendFireRecorded(p *provenance.Causal, deliveryID string) bool {
	if p == nil {
		return false
	}
	for _, fire := range p.FireKeys {
		if fire.DeliveryID == deliveryID {
			return true
		}
	}
	for _, entry := range p.Chain {
		if entry.Kind == "watch" && entry.DeliveryID == deliveryID {
			return true
		}
	}
	return false
}

// A coalescing plan prunes the superseded frame's fire from the survivor's
// ancestry because that frame can never count toward the fuse. The prune is a
// decision about a frame that stays the current pending frame in the runtime
// map until the replacement is durable and commits — an fsync and a refold
// later. A drain in that window must not deliver the frame the fuse can no
// longer see: if it does, the frame settles delivered while its fire is gone
// and the runaway fuse counts one fewer delivered ancestor than reality.
//
// The plan therefore takes the frame's delivery claim while it prunes, and the
// persist releases it once the replacement is published. This test drains the
// superseded frame inside the window, before its replacement's pending event is
// durable, and requires the reservation to refuse the claim.
func TestSupersededFramePrunedFireIsNotDeliverableBeforeItsReplacementCommits(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM

	// A delivered ancestor, so the survivor's ancestry holds a fire the fuse
	// must still count after the prune.
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "ancestor"})
	ancestor := fixture.requireOnePending(t).state
	if delivered, err := jm.deliverPendingWatchSend(cfg, ancestor, false); !delivered || err != nil {
		t.Fatalf("deliver the ancestor frame: delivered=%t err=%v", delivered, err)
	}

	// The frame whose fire the coalescing plan will prune.
	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventCommunicate,
		SessionID:  jm.sessionID,
		Data:       events.CommunicateData{Message: "superseded"},
		Provenance: provenance.Clone(ancestor.Provenance),
	})
	superseded := fixture.requireOnePending(t).state

	var inWindow []pendingWatchSendDelivery
	var drained bool
	var delivered bool
	var drainErr error
	originalAppend := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		// Before the replacement's pending event is durable, the durable fold
		// still names the superseded frame as the pending head, so a drain here
		// would hand the receiver a frame whose fire is already pruned.
		if !drained && event.Kind == jobstore.EventWatchSendPending &&
			event.WatchSend != nil && event.WatchSend.DeliveryID != superseded.DeliveryID && event.WatchSend.UpdateSeq > superseded.UpdateSeq {
			drained = true
			inWindow = jm.pendingWatchSendDeliveries(nil)
			delivered, drainErr = jm.deliverPendingWatchSend(cfg, superseded, false)
		}
		return originalAppend(event)
	}
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventCommunicate,
		SessionID: jm.sessionID,
		Data:      events.CommunicateData{Message: "survivor"},
		// A loop's next trigger descends from the frame the receiver actually
		// took, so the survivor's own lineage carries the delivered ancestor
		// and never the superseded frame the model never saw.
		Provenance: provenance.Clone(ancestor.Provenance),
	})
	jm.appendEvent = originalAppend
	if !drained {
		t.Fatal("the drain never ran inside the replacement's persistence window")
	}
	if len(inWindow) != 1 || inWindow[0].state.DeliveryID != superseded.DeliveryID {
		t.Fatalf("the window's pending frame = %+v, want the superseded frame alone", inWindow)
	}
	if drainErr != nil {
		t.Fatalf("drain the superseded frame in the window: %v", drainErr)
	}
	if delivered {
		t.Fatalf("the drain delivered frame %s after the plan pruned its fire from the survivor's ancestry", superseded.DeliveryID)
	}

	survivor := fixture.requireOnePending(t).state
	if watchSendFireRecorded(survivor.Provenance, superseded.DeliveryID) {
		t.Fatalf("the survivor's fire record still holds the pruned frame %s", superseded.DeliveryID)
	}
	// The invariant the prune exists to keep: every frame in the survivor's
	// ancestry that settled delivered is named in the fire record the fuse
	// counts. A frame delivered inside the window breaks it.
	jm.mu.Lock()
	for _, state := range []jobstore.WatchSendState{ancestor, superseded} {
		if jm.watchSendDeliveredLocked(state.DeliveryID) && !watchSendFireRecorded(survivor.Provenance, state.DeliveryID) {
			jm.mu.Unlock()
			t.Fatalf("frame %s settled delivered but the survivor's fire record cannot count it", state.DeliveryID)
		}
	}
	depth := jm.fuseDepthLocked(cfg, survivor.Provenance)
	jm.mu.Unlock()
	if depth != 1 {
		t.Fatalf("fuse depth over the survivor's ancestry = %d, want 1 (the delivered ancestor alone)", depth)
	}
	requireOnlyStableWatchReceipts(t, fixture, survivor.DeliveryID)
}

// The receiver takes the first frame, a replacement coalesces over it before
// its acknowledgement, and the acknowledgement fails. The accepted frame is
// no longer pending, so nothing would redeliver it: its receipt stays held
// for a settlement retry, which acknowledges it on the next pass.
func TestSupersededAcceptedFrameRetriesItsAcknowledgement(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	ackFailure := errors.New("acknowledgement failed")
	failed := false
	originalAppend := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if !failed && exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, first) {
			failed = true
			onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
			return ackFailure
		}
		return originalAppend(event)
	}
	_, err := jm.deliverStableWatchSend(cfg, first)
	jm.appendEvent = originalAppend
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if !errors.Is(err, ackFailure) {
		t.Fatalf("deliver first frame: err = %v, want the acknowledgement failure", err)
	}
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("the accepted, unacknowledged frame lost its receipt: %v", got)
	}
	requireSettlementRetryAcknowledges(t, fixture, first, second)
}

// A caller frame whose content is already in the receiver's transcript is
// certainly delivered: only its delivered record is outstanding, and the
// settle's last retry records it (completeWatchSendSettle) whether or not the
// journal ever accepted an earlier attempt. Coalescing over that frame may
// therefore not prune its fire from the survivor's ancestry. If it does, the
// fuse counts one fewer delivered ancestor than reality: the frame records
// delivered after its fire is gone.
func TestSupersededTranscribedCallerFrameKeepsItsFire(t *testing.T) {
	t.Parallel()
	s, jm, _ := installCallerCommunicateWatch(t)
	attempts := failAppendN(jm, jobstore.EventWatchSendDelivered, 1000)
	fireCommunicate(jm, "transcribed-first")
	drainAndAccept(t, s)
	first := jm.pendingWatchSendDeliveries(nil)
	if len(first) != 1 {
		t.Fatalf("pending frames after the failing settle = %d, want the transcribed frame alone", len(first))
	}
	transcribed := first[0].state
	if attempts.Load() == 0 {
		t.Fatal("test premise broken: the settle never failed, so the frame is not transcribed-but-unsettled")
	}
	if !jm.watchSendTranscribed(transcribed.DeliveryID) {
		t.Fatal("the frame that reached the transcript is not marked transcribed")
	}
	if jm.watchSendDelivered(transcribed.DeliveryID) {
		t.Fatal("test premise broken: a transcribed frame with a failing settle is already delivered")
	}

	fireCommunicate(jm, "transcribed-second")
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 {
		t.Fatalf("pending frames after the coalesce = %d, want 1", len(pending))
	}
	survivor := pending[0].state
	if survivor.DeliveryID == transcribed.DeliveryID {
		t.Fatal("test premise broken: the second fire did not coalesce over the transcribed frame")
	}
	if !watchSendFireRecorded(survivor.Provenance, transcribed.DeliveryID) {
		t.Fatalf("the coalesce pruned frame %s's fire, but its content is already in the receiver's transcript", transcribed.DeliveryID)
	}

	// The sting of that prune: the settle's last retry marks the frame
	// delivered. The survivor's fire record must be able to count it.
	cfg := onlyWatchConfigForTest(t, jm)
	jm.completeWatchSendSettle(cfg, transcribed)
	if !jm.watchSendDelivered(transcribed.DeliveryID) {
		t.Fatal("the transcribed frame did not settle delivered on its last retry")
	}
	jm.mu.Lock()
	depth := jm.fuseDepthLocked(cfg, survivor.Provenance)
	jm.mu.Unlock()
	if depth != 1 {
		t.Fatalf("fuse depth over the survivor's ancestry = %d, want 1 for the delivered transcribed frame", depth)
	}
}

// The first frame's deliverer passed its durable check before the
// replacement's pending append, handed the frame to the receiver, failed the
// acknowledgement and released its claim, all before the replacement's
// runtime commit. That commit then replaces a frame nobody claims but the
// receiver took: it hands the frame to the settlement retry rather than
// dropping its receipt. The deliverer's state at that point is a noted failed
// acknowledgement and no claim.
func TestAcceptedFrameReplacedAfterItsClaimRetriesItsAcknowledgement(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	jm.noteUnsettledWatchSend(cfg, first, false)
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("releasing a still-current claim dropped its receipt: %v", got)
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("the accepted, unacknowledged frame lost its receipt: %v", got)
	}
	requireSettlementRetryAcknowledges(t, fixture, first, second)
}

// A frame whose receipt was already released (its watch was cleared between
// the hand-off and the failed acknowledgement) is retired: noting the failed
// acknowledgement keeps nothing, so no note outlives it.
func TestFailedAcknowledgementOfRetiredFrameKeepsNoNote(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	jm.releaseStableWatchReceipt(first.DeliveryID)
	jm.noteUnsettledWatchSend(cfg, first, false)
	jm.mu.Lock()
	_, noted := jm.unsettledWatchSends[first.DeliveryID]
	jm.mu.Unlock()
	if noted {
		t.Fatal("a retired frame's failed acknowledgement was kept")
	}
}

// A drain that delivers the replacement frame as soon as the coalesce
// publishes it must find the frame's delivery receipt already promoted and
// reuse it. Otherwise the drain acquires a receipt of its own and the
// coalesce's later promotion makes a second one, which nothing releases. So
// the frame is never visible to drains without a remembered receipt.
func TestCoalescedFramePublishedOnlyAfterItsReceiptIsPromoted(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	visibleWithoutReceipt := false
	jm.watchReceiptBoundary = func() {
		for _, p := range jm.pendingWatchSendDeliveries(nil) {
			if p.state.DeliveryID != first.DeliveryID && jm.stableWatchReceipt(p.state.DeliveryID) == nil {
				visibleWithoutReceipt = true
			}
		}
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	jm.watchReceiptBoundary = nil
	if visibleWithoutReceipt {
		t.Fatal("the replacement was visible to drains before its receipt was promoted")
	}
	second := fixture.requireOnePending(t).state
	if jm.stableWatchReceipt(second.DeliveryID) == nil {
		t.Fatal("the published replacement has no remembered receipt for a drain to reuse")
	}
	if _, err := jm.deliverPendingWatchSend(cfg, second, false); err != nil {
		t.Fatalf("deliver replacement: %v", err)
	}
	requireOnlyStableWatchReceipts(t, fixture)
}

// The receiver took the first frame but its acknowledgement failed. On the
// redelivery a replacement coalesces over it while the receiver's repeated
// append runs, and that append fails. The frame was still accepted the first
// time, so it keeps its receipt and goes to the settlement retry when its
// claim is released.
func TestAcceptedFrameKeepsItsRetryThroughAFailedSupersededRedelivery(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	rootFS := attachEnvironmentFailureFS(t, fixture.root)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	jm.noteUnsettledWatchSend(cfg, first, false)
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	appendFailure := errors.New("receiver append failed")
	rootFS.mu.Lock()
	rootFS.writeFailure = appendFailure
	rootFS.onWriteFailure = func() {
		onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	}
	rootFS.mu.Unlock()
	_, err := jm.deliverStableWatchSend(cfg, first)
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if !errors.Is(err, appendFailure) {
		t.Fatalf("redelivery: err = %v, want the receiver append failure", err)
	}
	second := fixture.requireOnePending(t).state
	got := stableWatchDeliveryReceiptIDs(fixture.controller)
	if !slices.Contains(got, first.DeliveryID) || !slices.Contains(got, second.DeliveryID) {
		t.Fatalf("delivery receipts = %v, want the accepted frame's kept beside %s", got, second.DeliveryID)
	}
	if !stableWatchSettlementRetrying(jm, first.DeliveryID) {
		t.Fatal("the accepted frame was not handed to the settlement retry")
	}
}

// The receiver takes the first frame but arming its attention fails, and a
// replacement then coalesces over it. Nothing will redeliver the first frame,
// so it keeps its receipt and goes to the settlement retry, which arms and
// acknowledges it.
func TestAcceptedFrameWithFailedArmRetriesAfterACoalesce(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	armFailure := errors.New("arm failed")
	jm.stableWatchArmFailure = func(string) error { return armFailure }
	if _, err := jm.deliverPendingWatchSend(cfg, first, false); !errors.Is(err, armFailure) {
		t.Fatalf("deliver first frame: err = %v, want the arm failure", err)
	}
	jm.stableWatchArmFailure = nil
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("the accepted, unarmed frame lost its receipt: %v", got)
	}
	requireSettlementRetryAcknowledges(t, fixture, first, second)
}

// The settlement retry for an accepted frame whose arm failed arms it before
// acknowledging it. While the arm keeps failing, the retry and the receipt
// stay; once it succeeds the frame is armed, acknowledged and released.
func TestSettlementRetryArmsBeforeItAcknowledges(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	armFailure := errors.New("arm failed")
	jm.stableWatchArmFailure = func(string) error { return armFailure }
	if _, err := jm.deliverPendingWatchSend(cfg, first, false); !errors.Is(err, armFailure) {
		t.Fatalf("deliver first frame: err = %v, want the arm failure", err)
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if _, err := jm.retryStableWatchSettlements(); !errors.Is(err, armFailure) {
		t.Fatalf("retry while the arm still fails: err = %v, want the arm failure", err)
	}
	retrying := stableWatchSettlementRetrying(jm, first.DeliveryID)
	if !retrying || jm.watchSendDelivered(first.DeliveryID) {
		t.Fatalf("after a failed retry arm: retrying=%t acknowledged=%t, want the retry kept and no acknowledgement", retrying, jm.watchSendDelivered(first.DeliveryID))
	}
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("a failed retry arm released the receipt: %v", got)
	}
	jm.stableWatchArmFailure = nil
	if _, err := jm.retryStableWatchSettlements(); err != nil {
		t.Fatalf("retry once the arm succeeds: %v", err)
	}
	if !jm.watchSendDelivered(first.DeliveryID) {
		t.Fatal("the retry did not acknowledge the frame")
	}
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}
