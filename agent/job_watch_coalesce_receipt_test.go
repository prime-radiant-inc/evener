package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
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

// acceptStableWatchWithFailedAck crosses real receiver custody before failing
// the exact source receipt append. It does not manufacture an acceptance note.
func acceptStableWatchWithFailedAck(t *testing.T, fixture *stableWatchRuntimeFixture, state jobstore.WatchSendState, retire bool) {
	t.Helper()
	jm := fixture.sourceJM
	failure := errors.New("source acknowledgement held after real custody")
	appendOne := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, state) {
			if retire {
				jm.releaseStableWatchReceipt(state.DeliveryID)
			}
			return failure
		}
		return appendOne(event)
	}
	_, err := jm.deliverStableWatchSend(fixture.onlyWatchConfig(t), state)
	jm.appendEvent = appendOne
	if !errors.Is(err, failure) {
		t.Fatalf("accepted frame source ack error=%v, want %v", err, failure)
	}
	entries := notificationRawEntries(t, fixture.rootTranscriptPath)
	if len(entries) != 1 || entries[0].Turn.AttentionID != stableWatchAttentionID(state) || entries[0].Turn.Message.Text() != stableWatchNotificationContent(state) {
		t.Fatal("failed ack did not follow exact real receiver custody")
	}
	if countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, state) != 0 {
		t.Fatal("failed acknowledgement became a source receipt")
	}
}

// When a second frame coalesces over a stable-receiver frame that is still
// pending, the superseded frame's delivery receipt must be released with it:
// only the frame that will be delivered keeps one. Otherwise the orphaned
// receipt fences the receiver's stop forever, and closing the root waits out
// the close budget.
func TestCoalescedStableWatchFrameReleasesSupersededReceipt(t *testing.T) {
	fs := &notificationFaultFS{Fs: afero.NewOsFs()}
	fixture := newStableWatchRuntimeFixture(t, fs)
	fs.writeErr = errors.New("receiver custody unavailable")
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if second.DeliveryID == first.DeliveryID {
		t.Fatalf("coalesced frame kept delivery id %s", first.DeliveryID)
	}
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
	fs.writeErr = nil
	fixture.sourceJM.dispatchProducedWatchSends()
	entries := notificationRawEntries(t, fixture.rootTranscriptPath)
	if len(entries) != 1 || entries[0].Turn.AttentionID != fmt.Sprintf("watch:%s:%d", second.DeliveryID, second.UpdateSeq) {
		t.Fatalf("survivor custody entries=%#v", entries)
	}
	requireOnlyStableWatchReceipts(t, fixture)
	if !fixture.sourceJM.watchSendDelivered(second.DeliveryID) {
		t.Fatal("surviving frame was not acknowledged")
	}
}

// A deliverer already handing off the first frame when the second coalesces
// over it keeps the first receipt through the supersede, then stands down
// without an error and releases it: the newer frame carries the content on.
func TestInFlightSupersededStableWatchFrameStandsDown(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	fixture.persistFrameBeforeTransport(t, "second")
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
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	fixture.persistFrameBeforeTransport(t, "second")
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
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	fixture.persistFrameBeforeTransport(t, "second")
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
	fixture.persistFrameBeforeTransport(t, "first")
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
	fixture.persistFrameBeforeTransport(t, "second")
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

// The receiver takes the first frame, a replacement coalesces over it before
// its acknowledgement, and the acknowledgement fails. The accepted frame is
// no longer pending, so nothing would redeliver it: its receipt stays held
// for a settlement retry, which acknowledges it on the next pass.
func TestSupersededAcceptedFrameRetriesItsAcknowledgement(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	fixture.persistFrameBeforeTransport(t, "first")
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
			fixture.persistFrameBeforeTransport(t, "second")
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

// The first frame's deliverer passed its durable check before the
// replacement's pending append, handed the frame to the receiver, failed the
// acknowledgement and released its claim, all before the replacement's
// runtime commit. That commit then replaces a frame nobody claims but the
// receiver took: it hands the frame to the settlement retry rather than
// dropping its receipt. The deliverer's state at that point is a noted failed
// acknowledgement and no claim.
func TestAcceptedFrameReplacedAfterItsClaimRetriesItsAcknowledgement(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	jm := fixture.sourceJM
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	acceptStableWatchWithFailedAck(t, fixture, first, false)
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("releasing a still-current claim dropped its receipt: %v", got)
	}
	fixture.persistFrameBeforeTransport(t, "second")
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
	jm := fixture.sourceJM
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	acceptStableWatchWithFailedAck(t, fixture, first, true)
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
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	visibleWithoutReceipt := false
	boundaryCalls := 0
	jm.watchReceiptBoundary = func() {
		boundaryCalls++
		for _, p := range jm.pendingWatchSendDeliveries(nil) {
			if p.state.DeliveryID != first.DeliveryID && jm.stableWatchReceipt(p.state.DeliveryID) == nil {
				visibleWithoutReceipt = true
			}
		}
	}
	fixture.persistFrameBeforeTransport(t, "second")
	jm.watchReceiptBoundary = nil
	if boundaryCalls == 0 {
		t.Fatal("replacement never crossed the receipt-promotion boundary")
	}
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
// redelivery a replacement coalesces over it while the receiver re-establishes
// duplicate durability, and that barrier fails. The frame was still accepted the first
// time, so it keeps its receipt and goes to the settlement retry when its
// claim is released.
func TestAcceptedFrameKeepsItsRetryThroughAFailedSupersededRedelivery(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	rootFS := attachEnvironmentFailureFS(t, fixture.root)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	acceptStableWatchWithFailedAck(t, fixture, first, false)
	fixture.root.cfg.testOnly.delegateAttentionOpenWriter = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(rootFS, path, id)
	}
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	appendFailure := errors.New("receiver duplicate durability failed")
	rootFS.mu.Lock()
	rootFS.failure = appendFailure
	rootFS.onFailure = func() {
		fixture.persistFrameBeforeTransport(t, "second")
	}
	rootFS.mu.Unlock()
	_, err := jm.deliverStableWatchSend(cfg, first)
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if !errors.Is(err, appendFailure) {
		t.Fatalf("redelivery: err = %v, want the receiver durability failure", err)
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
	fixture.persistFrameBeforeTransport(t, "first")
	first := fixture.requireOnePending(t).state
	armFailure := errors.New("arm failed")
	jm.stableWatchArmFailure = func(string) error { return armFailure }
	if _, err := jm.deliverPendingWatchSend(cfg, first, false); !errors.Is(err, armFailure) {
		t.Fatalf("deliver first frame: err = %v, want the arm failure", err)
	}
	attentionID := stableWatchAttentionID(first)
	fold, err := readDelegateAttentionFold(fixture.rootTranscriptPath, fixture.root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, accepted := fold.content[attentionID]; !accepted {
		t.Fatal("arm failed before the real receiver took durable custody")
	}
	fixture.persistFrameBeforeTransport(t, "second")
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Contains(got, first.DeliveryID) {
		t.Fatalf("the accepted, unarmed frame lost its receipt: %v", got)
	}
	if _, err := jm.retryStableWatchSettlements(); !errors.Is(err, armFailure) {
		t.Fatalf("settlement before arm repair: %v, want the retained arm failure", err)
	}
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, first); got != 0 {
		t.Fatalf("source acknowledgements before arm repair=%d, want zero", got)
	}
	jm.stableWatchArmFailure = nil
	requireSettlementRetryAcknowledges(t, fixture, first, second)
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, first); got != 1 {
		t.Fatalf("source acknowledgements after arm repair=%d, want one", got)
	}
	assertNotificationRecords(t, fixture.rootTranscriptPath, attentionID, fold.content[attentionID], 1)

	t.Run("real producer takes canonical custody without source staging", func(t *testing.T) {
		live := newStableWatchRuntimeFixture(t, nil)
		const body = "real-watch-producer-custody-73af"
		onSessionEventKD(live.sourceJM, events.EventCommunicate, events.CommunicateData{Message: body})
		sourceEvents := loadJobStoreEvents(t, live.sourceJM)
		var produced *jobstore.WatchSendState
		for _, event := range sourceEvents {
			if event.Kind == jobstore.EventWatchSendPending {
				produced = event.WatchSend
			}
		}
		if produced == nil || !strings.Contains(produced.Frame, body) {
			t.Fatal("real communicate event did not persist its source frame")
		}
		entries := notificationRawEntries(t, live.rootTranscriptPath)
		wantID := fmt.Sprintf("watch:%s:%d", produced.DeliveryID, produced.UpdateSeq)
		if len(entries) != 1 || entries[0].Turn.AttentionID != wantID || !strings.Contains(entries[0].Turn.Message.Text(), body) {
			t.Fatalf("real producer canonical receiver records=%+v, want one exact identity and payload", entries)
		}
		if got := countExactWatchSendEvents(sourceEvents, jobstore.EventWatchSendDelivered, *produced); got != 1 {
			t.Fatalf("real producer source acknowledgements=%d, want one after canonical custody", got)
		}
	})
}

// The settlement retry for an accepted frame whose arm failed arms it before
// acknowledging it. While the arm keeps failing, the retry and the receipt
// stay; once it succeeds the frame is armed, acknowledged and released.
func TestSettlementRetryArmsBeforeItAcknowledges(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	jm := fixture.sourceJM
	armFailure := errors.New("arm failed")
	jm.stableWatchArmFailure = func(string) error { return armFailure }
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
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
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, first); got != 0 {
		t.Fatalf("source acknowledgements before successful arm = %d, want 0", got)
	}
	jm.stableWatchArmFailure = nil
	if _, err := jm.retryStableWatchSettlements(); err != nil {
		t.Fatalf("retry once the arm succeeds: %v", err)
	}
	if !jm.watchSendDelivered(first.DeliveryID) {
		t.Fatal("the retry did not acknowledge the frame")
	}
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, first); got != 1 {
		t.Fatalf("source acknowledgements after successful arm = %d, want 1", got)
	}
	requireOnlyStableWatchReceipts(t, fixture, second.DeliveryID)
}
