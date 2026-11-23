package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Slice 2 of #5681/#7467: the delegatestore queue is the single authority for
// child-delegate pending attention, the row is consumed only when a settled
// request presents it, and a stop that lands before presentation leaves a
// durable hold rather than a transcript mirror.

func pendingAttentionRowsForTest(t *testing.T, c *delegateTreeController, delegateID string) []delegatestore.PendingAttention {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %q is absent from the durable state", delegateID)
	}
	return append([]delegatestore.PendingAttention(nil), aggregate.PendingAttention...)
}

func needsAttentionForTest(t *testing.T, c *delegateTreeController, delegateID string) bool {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %q is absent from the durable state", delegateID)
	}
	return aggregate.NeedsAttention
}

func acceptedTriggerForTest(t *testing.T, sess *Session) string {
	t.Helper()
	sess.attentionMu.Lock()
	defer sess.attentionMu.Unlock()
	return sess.attentionAcceptedID
}

func presentedTriggerForTest(t *testing.T, sess *Session) string {
	t.Helper()
	sess.attentionMu.Lock()
	defer sess.attentionMu.Unlock()
	return sess.attentionPresentedTrigger
}

// presentAttentionForTest drives the exact production promotion step: the
// accepted latch rides one settled non-delta request, which moves it to the
// presented slot and clears the accepted latch.
func presentAttentionForTest(t *testing.T, sess *Session, attentionID string) {
	t.Helper()
	sess.attentionMu.Lock()
	sess.attentionAcceptedID = attentionID
	sess.attentionAcceptedCause = nil
	sess.attentionStagedIDs = map[string]struct{}{attentionID: {}}
	sess.attentionMu.Unlock()
	sess.promoteStagedAttentionCoverage(llm.Request{})
	if got := presentedTriggerForTest(t, sess); got != attentionID {
		t.Fatalf("presented trigger = %q, want %q", got, attentionID)
	}
	if got := acceptedTriggerForTest(t, sess); got != "" {
		t.Fatalf("accepted latch = %q after presentation, want empty", got)
	}
}

func reopenDelegateControllerForTest(t *testing.T, journalPath string) *delegateTreeController {
	t.Helper()
	store, err := delegatestore.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen delegatestore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      filepath.Dir(journalPath),
		worktreeRoot:  filepath.Join(filepath.Dir(journalPath), "worktrees"),
		turnLimit:     1,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("reopen delegate controller: %v", err)
	}
	return c
}

// #5681 regression, through the real stop admission: a job_stop that lands
// between ReserveAttention and CommitStart must admit immediately and cancel
// the uncommitted reservation through the stop's start claim. It must not park
// on the reservation until the generation commits ahead of it. The row was
// never consumed, so it stays durably queued, no reservation survives to pin
// later drives, no generation launches, and a fresh wake starts normally.
func TestDelegateAttentionReserveCommitGapStopAbortsWithoutLeaking(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_gap"
		delegateID  = "dlg_target"
		attentionID = "attention-gap"
		freshID     = "delegate:fresh-after-stop"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	root := newAttentionRepairRoot(c.stateDir, nil)
	root.delegateController = c
	c.rootRuntime = root
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	// Before the fix the job_stop waited on this reservation's done channel and
	// never returned while the start stayed in the reserve-to-commit gap.
	result, cancelPlan, _, err := c.StopSubtreeAndDrive(context.Background(), rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("job_stop in the reserve-to-commit gap: %v", err)
	}
	if result.id != delegateID {
		t.Fatalf("job_stop target = %q, want %q", result.id, delegateID)
	}
	executeDelegateCancelPlan(cancelPlan)

	// While the reservation still blocks stop cleanup, nothing has consumed the
	// row: it is durably queued and unheld, and no generation has launched.
	c.mu.Lock()
	reservations := len(c.reservations)
	blocker := c.attentionStartBlockerLocked(delegateID)
	stillQueued := false
	for _, row := range c.durable[delegateID].PendingAttention {
		if row.AttentionID == attentionID && row.HeldThrough == 0 {
			stillQueued = true
		}
	}
	generation := c.durable[delegateID].Generation
	c.mu.Unlock()
	if reservations != 1 || blocker == nil {
		t.Fatalf("gap reservations = %d blocker=%v, want the start still blocking stop cleanup", reservations, blocker)
	}
	if !stillQueued {
		t.Fatal("the discarded row left the durable queue before the reservation was released")
	}
	if generation != 0 {
		t.Fatalf("delegate committed generation %d in the reserve-to-commit gap, want none", generation)
	}

	// The canceled reservation makes the commit refuse and release itself; the
	// drive's failure path aborts it again as a no-op.
	if _, err := c.CommitStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CommitStart after the stop = %v, want target busy", err)
	}
	if err := c.AbortStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("AbortStart after the refused commit = %v, want a released no-op", err)
	}

	// Join the stop's reconcile driver before reading the post-discard state:
	// only then is the discard final and the queue stable.
	c.mu.Lock()
	driver := c.stopDriver
	c.mu.Unlock()
	if driver == nil {
		t.Fatal("the admitted stop started no reconcile driver")
	}
	select {
	case <-driver.done:
	case <-time.After(5 * time.Second): // TRIPWIRE: an idle target reconciles immediately; only a stuck drain reaches this
		t.Fatal("the gap stop's reconcile driver did not drain")
	}

	// The stop discarded the never-consumed row in the journal alone, with no
	// generation behind it and nothing left runnable.
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	_, resolved := aggregate.ResolvedAttention[attentionID]
	pending := aggregateHasPendingAttention(aggregate, attentionID)
	needsAttention := aggregate.NeedsAttention
	generation = aggregate.Generation
	c.mu.Unlock()
	if !resolved {
		t.Fatalf("discarded gap row %q was not resolved in the journal", attentionID)
	}
	if pending {
		t.Fatalf("discarded gap row %q is still pending", attentionID)
	}
	if needsAttention {
		t.Fatal("NeedsAttention stayed true after the only row was discarded")
	}
	if generation != 0 {
		t.Fatalf("delegate committed generation %d, want the gap stop to launch none", generation)
	}
	if got := acceptedTriggerForTest(t, runtime); got != "" {
		t.Fatalf("accepted latch = %q after the aborted commit, want empty", got)
	}

	// Nothing pins the queue once the stop settles, and the discarded ID cannot
	// be reserved again: it is no longer an unheld pending row.
	if stale, err := c.ReserveAttention(runtime, attentionID); !errors.Is(err, errDelegateTargetBusy) || stale != nil {
		t.Fatalf("stale reserve of the discarded row = %#v, %v; want target busy", stale, err)
	}

	// A report queued after the stop reserves and starts normally.
	c.mu.Lock()
	_, err = c.appendLocked(attentionQueuedEvent(delegateID, freshID, true))
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("queue the fresh report: %v", err)
	}
	fresh, err := c.ReserveAttention(runtime, freshID)
	if err != nil {
		t.Fatalf("fresh wake after the gap stop: %v", err)
	}
	started, err := c.CommitStart(fresh)
	if err != nil {
		t.Fatalf("commit the fresh wake: %v", err)
	}
	if started.lease.generation != 1 {
		t.Fatalf("fresh wake generation = %d, want 1", started.lease.generation)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 1 || rows[0].AttentionID != freshID || rows[0].HeldThrough != 0 {
		t.Fatalf("pending rows after the fresh start = %#v, want only the unheld fresh report", rows)
	}
}

// All four #7484 scenarios that used to need transcript reopen mirrors, pinned
// on the single-store architecture.

// A mid-round user stop after the first settled request presented the trigger
// consumes exactly that row: no successor duplicate appears, and the in-memory
// presented slot is spent.
func TestDelegateAttentionPresentedUserStopConsumesOnlyThePresentedRow(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_presented_stop"
		delegateID  = "dlg_target"
		attentionID = "attention-presented-stop"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
	presentAttentionForTest(t, runtime, attentionID)
	if err := runtime.finishAttentionTurn(nil, errors.New("user stop")); err != nil {
		t.Fatalf("finishAttentionTurn after a presented stop: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 0 {
		t.Fatalf("pending rows after the presented stop = %#v, want the presented row consumed", rows)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true after the last presented row was consumed")
	}
	if got := presentedTriggerForTest(t, runtime); got != "" {
		t.Fatalf("presented slot = %q after the stop consumed it, want empty", got)
	}
}

// A mid-round stop after presentation with an earlier stop-held row: the
// presented row is consumed, the earlier row stays held in the durable queue,
// NeedsAttention is false so the root wires idle, and a transient resolution
// failure cannot strand NeedsAttention true.
func TestDelegateAttentionPresentedStopKeepsEarlierHeldRowQuiet(t *testing.T) {
	t.Parallel()
	const (
		sessionID  = "child-dlg_held_quiet"
		delegateID = "dlg_target"
		heldID     = "attention-earlier-held"
		presented  = "attention-presented"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, heldID, presented)
	runtime.owningDelegateID = delegateID
	if err := c.holdDelegateAttention(delegateID, heldID, 1); err != nil {
		t.Fatalf("hold the earlier row: %v", err)
	}
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, presented))
	presentAttentionForTest(t, runtime, presented)

	// A transient append failure leaves the presented row pending, so the
	// projection still counts it and nothing silently disappears.
	c.mu.Lock()
	c.appendFault = func(events []delegatestore.Event) error {
		for _, event := range events {
			if event.Kind == delegatestore.EventDelegateAttentionResolved {
				return errors.New("test: transient resolution failure")
			}
		}
		return nil
	}
	c.mu.Unlock()
	if err := runtime.finishAttentionTurn(nil, errors.New("user stop")); err != nil {
		t.Fatalf("finishAttentionTurn under a transient failure: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 2 {
		t.Fatalf("pending rows after the failed resolution = %#v, want both rows retained", rows)
	}
	if !needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention dropped although the presented row could not be resolved")
	}

	// The retry succeeds and the projection settles to the held row alone.
	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	if err := c.forgetDelegateAttention(delegateID, presented); err != nil {
		t.Fatalf("retry the presented row resolution: %v", err)
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != heldID || rows[0].HeldThrough != 1 {
		t.Fatalf("pending rows after the retry = %#v, want the earlier row held at generation 1", rows)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true with only a held row queued")
	}
	root := newSession(t)
	root.delegateController = c
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		c.rootRuntime = nil
		c.mu.Unlock()
		root.delegateController = nil
	})
	if got := root.WireState(); got != "idle" {
		t.Fatalf("root WireState with only a held child row = %q, want idle so the stopped delegate retires", got)
	}
}

// Tool-round exhaustion after a settled request presented the trigger consumes
// the presented row exactly once.
func TestDelegateAttentionPresentedToolRoundExhaustionConsumesTrigger(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_rounds"
		delegateID  = "dlg_target"
		attentionID = "attention-tool-rounds"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
	presentAttentionForTest(t, runtime, attentionID)
	if err := runtime.finishAttentionTurn(nil, errToolRoundExhaustedForTest()); err != nil {
		t.Fatalf("finishAttentionTurn after tool-round exhaustion: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 0 {
		t.Fatalf("pending rows after tool-round exhaustion = %#v, want the presented row consumed", rows)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true after the presented row was consumed")
	}
}

func errToolRoundExhaustedForTest() error { return errors.New("tool-round exhaustion") }

// A mid-round provider failure after the settled request presented the trigger
// consumes the presented row and returns root idle; a first-request failure
// before presentation keeps the row unheld and pending for the next drive.
func TestDelegateAttentionPresentedProviderFailureConsumesTrigger(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_provider"
		delegateID  = "dlg_target"
		attentionID = "attention-provider-failure"
	)
	t.Run("after presentation", func(t *testing.T) {
		c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		runtime.owningDelegateID = delegateID
		acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
		presentAttentionForTest(t, runtime, attentionID)
		if err := runtime.finishAttentionTurn(nil, errors.New("provider failure")); err != nil {
			t.Fatalf("finishAttentionTurn after a presented provider failure: %v", err)
		}
		if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 0 {
			t.Fatalf("pending rows after the presented failure = %#v, want the presented row consumed", rows)
		}
		if needsAttentionForTest(t, c, delegateID) {
			t.Fatal("NeedsAttention stayed true after the presented row was consumed")
		}
	})
	t.Run("before presentation", func(t *testing.T) {
		c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		runtime.owningDelegateID = delegateID
		acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
		runtime.settleUnpresentedDelegateTrigger(0, "", true)
		rows := pendingAttentionRowsForTest(t, c, delegateID)
		if len(rows) != 1 || rows[0].AttentionID != attentionID || rows[0].HeldThrough != 0 {
			t.Fatalf("pending rows after an unpresented transient failure = %#v, want the row unheld for retry", rows)
		}
		if !needsAttentionForTest(t, c, delegateID) {
			t.Fatal("NeedsAttention dropped although the unpresented row must retry")
		}
	})
}

// A stop on an unpresented trigger stamps a durable hold that survives a cold
// restore of the store, and a fresh committed wake clears it.
func TestDelegateAttentionHeldRowSurvivesColdRestoreAndReleases(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_cold_hold"
		delegateID  = "dlg_target"
		attentionID = "attention-cold-hold"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	journalPath := filepath.Join(c.stateDir, "delegate-events.jsonl")
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
	runtime.settleUnpresentedDelegateTrigger(1, "", true)
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].HeldThrough != 1 {
		t.Fatalf("pending rows after the stop = %#v, want the row held at generation 1", rows)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true after the only row was held")
	}

	restored := reopenDelegateControllerForTest(t, journalPath)
	rows = pendingAttentionRowsForTest(t, restored, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != attentionID || rows[0].HeldThrough != 1 {
		t.Fatalf("restored pending rows = %#v, want the held row read straight from the store", rows)
	}
	if needsAttentionForTest(t, restored, delegateID) {
		t.Fatal("restored NeedsAttention = true with only a held row")
	}

	var fresh []delegatestore.Event
	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	fresh = append(fresh, delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionQueued,
		DelegateID: delegateID,
		AttentionQueued: &delegatestore.AttentionQueued{
			AttentionID: "attention-fresh-wake",
			Committed:   true,
		},
	})
	restored.mu.Lock()
	if _, err := restored.appendLocked(fresh...); err != nil {
		restored.mu.Unlock()
		t.Fatalf("append the fresh wake: %v", err)
	}
	restored.mu.Unlock()
	rows = pendingAttentionRowsForTest(t, restored, delegateID)
	if len(rows) != 2 {
		t.Fatalf("pending rows after the fresh wake = %#v, want both rows", rows)
	}
	for _, row := range rows {
		if row.HeldThrough != 0 {
			t.Fatalf("row %q held through %d after a fresh committed wake, want the hold cleared", row.AttentionID, row.HeldThrough)
		}
	}
	if !needsAttentionForTest(t, restored, delegateID) {
		t.Fatal("NeedsAttention was not raised by the fresh wake")
	}
}

// A stop holds a coordinator's report in delegatestore, and a compaction that
// runs while the hold stands keeps only unheld rows, cutting the report's turn
// from resident history. A committed send clears the holds, so its generation
// must present the report: the send restores the missing turn from the
// transcript, in arrival order, before its first request is built.
func TestCommittedSendRestoresAHeldReportCompactedOutOfResidentHistory(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	var requests atomic.Int32
	var sawReport, reportFirst, firstRequest atomic.Bool
	handle := func(req llm.Request) llm.Response {
		if !requestMessagesContainText(req.Messages, "new instruction") {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
		}
		requests.Add(1)
		if !firstRequest.CompareAndSwap(false, true) {
			return agenttest.FinalResponse("send report")
		}
		reportAt, instructionAt := -1, -1
		for i, message := range req.Messages {
			switch {
			case strings.Contains(message.Text(), "HELD_REPORT"):
				reportAt = i
			case strings.Contains(message.Text(), "new instruction"):
				instructionAt = i
			}
		}
		sawReport.Store(reportAt >= 0)
		reportFirst.Store(reportAt >= 0 && instructionAt >= 0 && reportAt < instructionAt)
		return agenttest.FinalResponse("send report")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		handle, handle, handle,
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	const attentionID = "delegate:dlg_held/report/1"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "HELD_REPORT: the drain cursor is in the jobs journal"); err != nil || !appended {
		t.Fatalf("append the held report = appended:%t err:%v", appended, err)
	}
	// The finalize tail of a generation a user stop interrupted before its
	// first settled request leaves exactly this state: the accepted trigger
	// latched, then held at the stop's generation and released from the latch.
	sub.sess.holdAcceptedTrigger(attentionID)
	sub.sess.settleUnpresentedDelegateTrigger(2, "", true)
	if rows := pendingAttentionRowsForTest(t, root.delegateController, fixture.delegateID); len(rows) != 1 || rows[0].HeldThrough != 2 {
		t.Fatalf("pending rows after the stop = %#v, want the report held", rows)
	}
	// A compaction while the hold stands keeps only unheld rows, so it cuts the
	// held report's steering turn from the resident history.
	compactAfterAttention(t, sub.sess)
	sub.sess.mu.Lock()
	resident := false
	for _, turn := range sub.sess.history {
		if turn.AttentionID == attentionID {
			resident = true
		}
	}
	sub.sess.mu.Unlock()
	if resident {
		t.Fatal("the held report's turn survived compaction, so the restore path is untested")
	}

	sendAndAcknowledge(t, root, fixture.delegateID, "new instruction: continue")
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sawReport.Load() {
		t.Fatal("the send's first model request omitted the held report its commit released")
	}
	if !reportFirst.Load() {
		t.Fatal("the send's first model request presented the restored report after the new instruction")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("the send made %d model requests, want exactly one", got)
	}
	if rows := pendingAttentionRowsForTest(t, root.delegateController, fixture.delegateID); len(rows) != 0 {
		t.Fatalf("pending rows after the send = %#v, want the report consumed", rows)
	}
}

// A presented row whose journal resolution append failed must not trigger a
// second model request: the drive and the finalization drain withhold it while
// the retry is outstanding, and one successful retry removes it from the store.
// Rows the model never saw stay selectable throughout.
func TestDelegateAttentionAwaitingResolutionRetryRemovesOnlyThePresentedRow(t *testing.T) {
	t.Parallel()
	const (
		sessionID  = "child-dlg_awaiting_resolution"
		delegateID = "dlg_target"
	)
	presented := delegateAttentionID(delegatestore.UpdateDeliveryID(delegateID, 1))
	unpresented := "attention-never-presented"
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, presented, unpresented)
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, presented))
	presentAttentionForTest(t, runtime, presented)

	c.mu.Lock()
	c.appendFault = func(events []delegatestore.Event) error {
		for _, event := range events {
			if event.Kind == delegatestore.EventDelegateAttentionResolved {
				return errors.New("test: journal resolution failure")
			}
		}
		return nil
	}
	c.mu.Unlock()
	if err := runtime.finishAttentionTurn(nil, nil); err != nil {
		t.Fatalf("finishAttentionTurn with a failing resolution append: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 2 {
		t.Fatalf("pending rows after the failed resolution = %#v, want both rows retained", rows)
	}
	if !needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention dropped although the presented row could not be resolved")
	}
	// The model already read the presented update, so neither the attention
	// drive nor the finalization drain may make a request for it.
	if ids, err := runtime.automaticDelegateAttentionIDs(); err != nil || !slices.Equal(ids, []string{unpresented}) {
		t.Fatalf("drive candidates while awaiting the retry = %v (%v), want only %q", ids, err, unpresented)
	}
	// A coordinator parked at a quiet yield drains every pending row, so the
	// unpresented one stays owed while the presented one is withheld.
	if ids := runtime.pendingDelegateUpdateAttentionIDs(); !slices.Equal(ids, []string{unpresented}) {
		t.Fatalf("drain ids while awaiting the retry = %v, want only %q", ids, unpresented)
	}

	// The journal recovers: the retry removes the presented row and leaves the
	// unpresented one selectable.
	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	if err := runtime.flushAwaitingDelegateAttentionResolutions(); err != nil {
		t.Fatalf("flushAwaitingDelegateAttentionResolutions: %v", err)
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != unpresented {
		t.Fatalf("pending rows after the retry = %#v, want only %q", rows, unpresented)
	}
	if ids, err := runtime.automaticDelegateAttentionIDs(); err != nil || !slices.Equal(ids, []string{unpresented}) {
		t.Fatalf("drive candidates after the retry = %v (%v), want only %q", ids, err, unpresented)
	}
	if err := runtime.flushAwaitingDelegateAttentionResolutions(); err != nil {
		t.Fatalf("second flush after the retry: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 1 || rows[0].AttentionID != unpresented {
		t.Fatalf("pending rows after a second flush = %#v, want the retry to be a no-op", rows)
	}
}

// Pending attention turns reach a request in transcript arrival order, so an
// update that arrived before a later report is presented first even though the
// report's id sorts before the update's.
func TestPendingAttentionTurnsKeepTranscriptArrivalOrder(t *testing.T) {
	t.Parallel()
	const (
		sessionID  = "child-dlg_arrival_order"
		delegateID = "dlg_target"
	)
	updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(delegateID, 1))
	reportID := delegateAttentionID(delegateDeliveryID(delegateID, 1))
	if !sort.StringsAreSorted([]string{reportID, updateID}) {
		t.Fatalf("test setup: %q should sort before %q, so sorted selection cannot prove arrival order", reportID, updateID)
	}
	_, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, updateID, reportID)
	runtime.owningDelegateID = delegateID
	runtime.attentionMu.Lock()
	turns, err := runtime.pendingAttentionTurnsLocked()
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("pendingAttentionTurnsLocked: %v", err)
	}
	got := make([]string, 0, len(turns))
	for _, turn := range turns {
		got = append(got, turn.AttentionID)
	}
	if !slices.Equal(got, []string{updateID, reportID}) {
		t.Fatalf("pending attention turns = %v, want the update %q before the report %q", got, updateID, reportID)
	}
}

// The restore of compacted-out attention turns keeps transcript arrival order
// too: the caller's ids are id-sorted, so the delivery sorts before the earlier
// update, yet the update must be restored before the resident delivery.
func TestRetainPendingAttentionTurnsKeepTranscriptArrivalOrder(t *testing.T) {
	t.Parallel()
	const (
		sessionID  = "child-dlg_retain_order"
		delegateID = "dlg_target"
	)
	updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(delegateID, 1))
	reportID := delegateAttentionID(delegateDeliveryID(delegateID, 1))
	ids := []string{reportID, updateID}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("test setup: %v is not the drive's id-sorted selection", ids)
	}
	_, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, updateID, reportID)
	runtime.owningDelegateID = delegateID
	runtime.attentionMu.Lock()
	fold, err := runtime.delegateAttentionFoldLocked()
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("delegateAttentionFoldLocked: %v", err)
	}
	// A compaction kept the later delivery resident and cut the earlier update.
	if err := runtime.retainDelegateAttentionTurn(fold.turns[reportID]); err != nil {
		t.Fatalf("retain the resident delivery: %v", err)
	}
	runtime.attentionMu.Lock()
	err = runtime.retainPendingAttentionTurnsLocked(fold, ids)
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("retainPendingAttentionTurnsLocked: %v", err)
	}
	runtime.mu.Lock()
	got := make([]string, 0, len(runtime.history))
	for _, turn := range runtime.history {
		if turn.AttentionID != "" {
			got = append(got, turn.AttentionID)
		}
	}
	runtime.mu.Unlock()
	if !slices.Equal(got, []string{updateID, reportID}) {
		t.Fatalf("restored resident turns = %v, want the update %q before the report %q", got, updateID, reportID)
	}
}

// A stop that settles an attention generation before its first request
// presented carries the unpresented trigger's hold in the same durable batch as
// the stopped outcome. A journal that accepted the outcome therefore cannot
// leave the row unheld and runnable for the ordinary drive.
func TestStopFinishCarriesTheUnpresentedHoldAtomically(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_atomic_hold"
		delegateID  = "dlg_target"
		attentionID = "attention-atomic-hold"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	reservation := mustReserveAttentionForTest(t, c, runtime, attentionID)
	acceptAttentionForTest(t, runtime, reservation)
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	var sawOutcomeAndHold atomic.Bool
	c.mu.Lock()
	c.appendFault = func(events []delegatestore.Event) error {
		outcome, hold := false, false
		for _, event := range events {
			if event.RunFinished != nil {
				outcome = true
			}
			if event.AttentionHeld != nil && event.AttentionHeld.AttentionID == attentionID {
				hold = true
			}
		}
		if outcome && hold {
			sawOutcomeAndHold.Store(true)
		}
		return nil
	}
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		c.appendFault = nil
		c.mu.Unlock()
	})
	if _, err := c.FinishGeneration(started.lease, delegateFinish{
		outcome:         delegatestore.OutcomeStopped,
		disposition:     delegatestore.DispositionTerminalError,
		holdAttentionID: attentionID,
	}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if !sawOutcomeAndHold.Load() {
		t.Fatal("the stop finish batch did not carry the held trigger with its outcome")
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != attentionID || rows[0].HeldThrough != started.lease.generation {
		t.Fatalf("pending rows after the stop finish = %#v, want the trigger held at generation %d", rows, started.lease.generation)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true after the finish batch held its only row")
	}
}

// A SubagentStart hook stops an attention launch before it reaches the model,
// so the run finishes Completed while its accepted trigger was never presented.
// The hold must ride that finish too, or the drive relaunches the row into the
// same hook forever.
func TestCompletedFinishStillCarriesAnUnpresentedHold(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_completed_hold"
		delegateID  = "dlg_target"
		attentionID = "attention-completed-hold"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	reservation := mustReserveAttentionForTest(t, c, runtime, attentionID)
	acceptAttentionForTest(t, runtime, reservation)
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		packet: &delegatestore.TerminalPacket{
			Kind:    delegatestore.PacketReported,
			Message: json.RawMessage(`"done"`),
		},
		holdAttentionID: attentionID,
	}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != attentionID || rows[0].HeldThrough != started.lease.generation {
		t.Fatalf("pending rows after the completed finish = %#v, want the trigger held at generation %d", rows, started.lease.generation)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true after the completed finish batch held its only row")
	}
}

// When the finish batch itself fails, the accepted trigger stays latched: the
// ordinary drive must not accept the stopped row again before settlement
// recovery retries the batch that carries the hold. Other unheld rows remain
// eligible, so the failure withholds only this trigger.
func TestFailedStopFinishBatchKeepsTheAcceptedTriggerLatched(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_failed_stop_batch"
		delegateID  = "dlg_target"
		attentionID = "attention-failed-stop-batch"
		otherID     = "attention-other-pending"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
	c.mu.Lock()
	if _, err := c.appendLocked(attentionQueuedEvent(delegateID, otherID, true)); err != nil {
		c.mu.Unlock()
		t.Fatalf("queue the other pending row: %v", err)
	}
	c.mu.Unlock()

	runtime.settleUnpresentedDelegateTrigger(1, attentionID, false)
	if got := runtime.acceptedDelegateAttentionTriggerID(); got != attentionID {
		t.Fatalf("accepted trigger after a failed finish batch = %q, want %q kept latched", got, attentionID)
	}
	pending, err := runtime.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention ids: %v", err)
	}
	if !slices.Contains(pending, otherID) {
		t.Fatalf("pending attention = %v, want the unrelated unheld row %q still eligible", pending, otherID)
	}

	runtime.settleUnpresentedDelegateTrigger(1, attentionID, true)
	if got := runtime.acceptedDelegateAttentionTriggerID(); got != "" {
		t.Fatalf("accepted trigger after the finish batch landed = %q, want cleared", got)
	}
}

func mustReserveAttentionForTest(t *testing.T, c *delegateTreeController, sess *Session, attentionID string) *delegateStartReservation {
	t.Helper()
	reservation, err := c.ReserveAttention(sess, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention(%q): %v", attentionID, err)
	}
	return reservation
}

// A stop-held update row must stay out of the finalization drain's predicates.
// The drain runs a leased notification turn for every update it reads as owed,
// but a leased request filters held rows out, so a held row it admitted would
// neither be presented nor consumed and the drain would burn model calls
// forever. Held rows leave the queue predicates entirely.
func TestHeldDelegateAttentionStaysOutOfTheFinalizationDrain(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_drain_hold"
		delegateID  = "dlg_target"
		updateIDTip = 1
	)
	attentionID := delegatestore.UpdateDeliveryID(delegateID, updateIDTip)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	acceptAttentionForTest(t, runtime, mustReserveAttentionForTest(t, c, runtime, attentionID))
	runtime.settleUnpresentedDelegateTrigger(1, "", true)

	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 1 || rows[0].HeldThrough == 0 {
		t.Fatalf("pending rows after the stop = %#v, want one held row", rows)
	}
	if needsAttentionForTest(t, c, delegateID) {
		t.Fatal("NeedsAttention stayed true for a stop-held update")
	}
	if pending, err := runtime.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending attention = %v (%v), want the held row filtered out", pending, err)
	}
	if runtime.hasPendingDelegateUpdateAttention() {
		t.Fatal("the finalization drain still reads the held update as owed")
	}
	if ids := runtime.pendingDelegateUpdateAttentionIDs(); len(ids) != 0 {
		t.Fatalf("drain update ids = %v, want none for a held row", ids)
	}
}

// Legacy read-only compatibility: a delegates.jsonl written before Slice 2
// still opens and folds, and a transcript holding a child TurnAttentionResolution
// still loads.
func TestLegacyDelegateAttentionJournalAndTranscriptStillRead(t *testing.T) {
	t.Parallel()
	t.Run("legacy journal", func(t *testing.T) {
		dir := t.TempDir()
		journalPath := filepath.Join(dir, "delegates.jsonl")
		store, err := delegatestore.Open(journalPath)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		state := delegatestore.State{}
		created := delegateControllerCreatedEvent("dlg_legacy", "")
		created.Created.Descriptor.OwnerSessionID = "root-session"
		started := delegateControllerRunStartedEvent("dlg_legacy", 1, delegatestore.TriggerAttention, time.Unix(100, 0).UTC())
		finished := delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_legacy",
			RunFinished: &delegatestore.RunFinished{
				Generation:  1,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCompleted, EndedAt: time.Unix(101, 0).UTC()},
				Disposition: delegatestore.DispositionCompletedNoAction,
			},
		}
		for _, event := range []delegatestore.Event{
			created,
			started,
			finished,
			{
				Kind:       delegatestore.EventDelegateAttentionChanged,
				DelegateID: "dlg_legacy",
				AttentionChanged: &delegatestore.DelegateAttentionChanged{
					NeedsAttention: true,
				},
			},
			{
				Kind:       delegatestore.EventDelegateAttentionHoldReleased,
				DelegateID: "dlg_legacy",
				AttentionHoldReleased: &delegatestore.AttentionHoldReleased{
					Generation: 1,
				},
			},
		} {
			_, state, err = store.Append(state, event)
			if err != nil {
				t.Fatalf("append legacy %q: %v", event.Kind, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		reopened := reopenDelegateControllerForTest(t, journalPath)
		if !needsAttentionForTest(t, reopened, "dlg_legacy") {
			t.Fatal("legacy EventDelegateAttentionChanged no longer folds NeedsAttention true")
		}
	})

	t.Run("legacy transcript resolution", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "legacy-child.jsonl")
		const sessionID = "child-legacy"
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		steering := schema.NewTurn(schema.TurnSteering, llm.User("legacy attention"))
		steering.AttentionID = "legacy-attention"
		if err := writer.AppendDurable(steering); err != nil {
			t.Fatalf("append legacy steering: %v", err)
		}
		resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.System("Attention resolved."))
		resolution.AttentionID = ""
		resolution.AttentionResolution = &schema.AttentionResolutionInfo{
			AttentionID:      "legacy-attention",
			Disposition:      "consumed",
			ResumeGeneration: 1,
		}
		if err := writer.AppendDurable(resolution); err != nil {
			t.Fatalf("append legacy resolution: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read legacy attention fold: %v", err)
		}
		if got := fold.resolutions["legacy-attention"]; got != delegateAttentionConsumed {
			t.Fatalf("legacy resolution = %q, want consumed", got)
		}
		if fold.resumeGenerations["legacy-attention"] != 1 {
			t.Fatalf("legacy resume generation = %d, want 1", fold.resumeGenerations["legacy-attention"])
		}
	})
}

// A delivery that writes its transcript message and then fails to journal the
// queue row must not ack. Its retry has to repair the row, because the message
// being present says nothing about what the delegate still owes; the repair
// still refuses to resurrect a row the journal already resolved.
func TestDelegateAttentionRetryRepairsAMissingQueueRow(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_repair"
		delegateID  = "dlg_target"
		attentionID = "delegate:repair"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil)
	runtime.owningDelegateID = delegateID
	c.mu.Lock()
	c.appendFault = func(events []delegatestore.Event) error {
		for _, event := range events {
			if event.Kind == delegatestore.EventDelegateAttentionQueued {
				return errors.New("test: transient queue append failure")
			}
		}
		return nil
	}
	c.mu.Unlock()
	if _, err := runtime.appendDelegateNotificationDurably(attentionID, "repair sentinel"); err == nil {
		t.Fatal("the delivery acknowledged a message whose queue row never landed")
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 0 {
		t.Fatalf("queue rows after the failed journal append = %#v, want none", rows)
	}

	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	appended, err := runtime.appendDelegateNotificationDurably(attentionID, "repair sentinel")
	if err != nil {
		t.Fatalf("retry after journal recovery: %v", err)
	}
	if appended {
		t.Fatal("the retry re-appended a message the transcript already held")
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != attentionID {
		t.Fatalf("queue rows after the repair = %#v, want the repaired row", rows)
	}
	if !needsAttentionForTest(t, c, delegateID) {
		t.Fatal("the repaired row raised no NeedsAttention")
	}
	c.mu.Lock()
	selected, selectedID, pending := c.nextIdleDelegateAttentionLocked()
	c.mu.Unlock()
	if !pending || selected != delegateID || selectedID != attentionID {
		t.Fatalf("drive selection after the repair = (%q, %q, %t), want the repaired row", selected, selectedID, pending)
	}

	// The same route after a resolution must stay a no-op: a presented and
	// answered report is never re-queued by a repeated delivery.
	if err := c.forgetDelegateAttention(delegateID, attentionID); err != nil {
		t.Fatalf("resolve the repaired row: %v", err)
	}
	if _, err := runtime.appendDelegateNotificationDurably(attentionID, "repair sentinel"); err != nil {
		t.Fatalf("repeat route after resolution: %v", err)
	}
	if rows := pendingAttentionRowsForTest(t, c, delegateID); len(rows) != 0 {
		t.Fatalf("queue rows after a repeat route of a resolved message = %#v, want none", rows)
	}
}
