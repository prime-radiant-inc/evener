package agent

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

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

// #5681 regression: a stop that discards the pending row between ReserveAttention
// and CommitStart must abort the reservation cleanly. The row was never
// consumed, so it stays durably queued, no reservation survives to pin later
// drives, and no in-memory latch claims a generation that never ran.
func TestDelegateAttentionReserveCommitGapStopAbortsWithoutLeaking(t *testing.T) {
	t.Parallel()
	const (
		sessionID   = "child-dlg_gap"
		delegateID  = "dlg_target"
		attentionID = "attention-gap"
	)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	runtime.owningDelegateID = delegateID
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	// A stop landing in the gap cancels the reservation's run context; the
	// commit re-reads it under the lock and must abort there.
	c.mu.Lock()
	record, err := c.reservationRecordLocked(reservation)
	if err != nil || record == nil || record.cancel == nil {
		c.mu.Unlock()
		t.Fatalf("reservation record = %#v, err %v", record, err)
	}
	record.cancel()
	c.mu.Unlock()
	if _, err := c.CommitStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CommitStart after the stop = %v, want target busy", err)
	}
	// With the reservation aborted, the stop admission no longer waits on it.
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), delegateID); err != nil {
		t.Fatalf("StopSubtree after the aborted commit: %v", err)
	}
	if len(c.reservations) != 0 {
		t.Fatalf("reservations after the aborted commit = %d, want the gap stop to release every one", len(c.reservations))
	}
	rows := pendingAttentionRowsForTest(t, c, delegateID)
	if len(rows) != 1 || rows[0].AttentionID != attentionID {
		t.Fatalf("pending rows after the gap stop = %#v, want the untouched row", rows)
	}
	if rows[0].HeldThrough != 0 {
		t.Fatalf("gap stop stamped hold %d on a row no generation presented, want unheld", rows[0].HeldThrough)
	}
	if got := acceptedTriggerForTest(t, runtime); got != "" {
		t.Fatalf("accepted latch = %q after the aborted commit, want empty", got)
	}
	c.mu.Lock()
	blocker := c.attentionStartBlockerLocked(delegateID)
	_, stillQueued := c.delegateAttentionWakeIDsLocked(delegateID)[attentionID]
	c.mu.Unlock()
	if blocker != nil {
		t.Fatal("the aborted reservation still blocks attention starts")
	}
	if !stillQueued {
		t.Fatal("the discarded row left the durable queue: nothing was consumed, so nothing may be lost")
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
		runtime.settleUnpresentedDelegateTrigger(0)
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
	runtime.settleUnpresentedDelegateTrigger(1)
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
	runtime.settleUnpresentedDelegateTrigger(1)

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
