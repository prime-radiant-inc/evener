package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestClientMutation_QueuePublicPathUsesDurableAuthority(t *testing.T) {
	sess := newTestSession(t)

	if err := sess.Enqueue(context.Background(), "durable follow-up"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("durable queue length = %d, want 1", len(snapshot.InputQueue))
	}
	if snapshot.QueueRevision != 1 {
		t.Fatalf("durable queue revision = %d, want 1", snapshot.QueueRevision)
	}
	entry := snapshot.InputQueue[0]
	if entry.ID == "" {
		t.Fatal("durable queue entry has no stable ID")
	}
	if entry.ClientMutationID == "" {
		t.Fatal("durable queue entry has no client mutation ID")
	}
	if len(entry.Input) != 1 || entry.Input[0].Type != "text" || entry.Input[0].Text != "durable follow-up" {
		t.Fatalf("durable queue input = %#v, want text input", entry.Input)
	}
}

func TestClientMutation_InputShapesMatchDaemonBoundary(t *testing.T) {
	invalidInputs := []struct {
		name  string
		input []appwire.InputItem
	}{
		{"empty type", []appwire.InputItem{{Text: "legacy"}}},
		{"legacy text", []appwire.InputItem{{Type: "input_text", Text: "legacy"}}},
		{"legacy image", []appwire.InputItem{{Type: "input_image", Data: []byte("legacy")}}},
		{"unsupported", []appwire.InputItem{{Type: "audio", Data: []byte("unsupported")}}},
	}
	for _, invalid := range invalidInputs {
		t.Run(invalid.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()
			setTestClientMutationActiveTurn(t, sess, "turn_1")
			calls := []struct {
				name string
				call func() error
			}{
				{"start", func() error {
					_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "start", Input: invalid.input})
					return err
				}},
				{"steer", func() error {
					_, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "steer", ExpectedTurnID: "turn_1", Input: invalid.input})
					return err
				}},
				{"queue", func() error {
					_, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "queue", ExpectedTurnID: "turn_1", Input: invalid.input})
					return err
				}},
				{"drain", func() error {
					_, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "drain", ExpectedTurnID: "turn_1", Input: invalid.input})
					return err
				}},
			}
			for _, call := range calls {
				t.Run(call.name, func(t *testing.T) {
					err := call.call()
					var wire appwire.WireError
					if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
						t.Fatalf("error = %T %v, want InvalidParams", err, err)
					}
				})
			}
			if snapshot := sess.clientMutations.snapshot(); len(snapshot.Journal) != 0 {
				t.Fatalf("invalid inputs reached durable journal: %#v", snapshot.Journal)
			}
		})
	}
}

func TestClientMutation_DrainRejectsSemanticallyEmptyExtraInputWithEmptyQueue(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	setTestClientMutationActiveTurn(t, sess, "turn_1")

	_, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "empty-drain",
		ExpectedTurnID:        "turn_1",
		ExpectedQueueRevision: 0,
		Input:                 []appwire.InputItem{{Type: "text", Text: " \t\n "}},
	})
	assertClientMutationConflict(t, err)
	if steering := sess.SteeringQueueSnapshot(); len(steering) != 0 {
		t.Fatalf("empty drain produced steering: %#v", steering)
	}
}

func TestClientMutation_CanonicalTextAndImagePayloadsArePreserved(t *testing.T) {
	input := []appwire.InputItem{
		{Type: "text", Text: "canonical text"},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "proof.png", Metadata: map[string]string{"source": "test"}},
	}
	assertInput := func(t *testing.T, got []appwire.InputItem) {
		t.Helper()
		if len(got) != 2 || got[0].Type != "text" || got[0].Text != "canonical text" ||
			got[1].Type != "image" || got[1].MediaType != "image/png" || got[1].Name != "proof.png" ||
			!slices.Equal(got[1].Data, []byte{1, 2, 3}) || got[1].Metadata["source"] != "test" {
			t.Fatalf("canonical payload = %#v", got)
		}
	}

	t.Run("start", func(t *testing.T) {
		sess := newQueuePersistTestSession(t, t.TempDir())
		defer sess.Close()
		if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "canonical-start", Input: input}); err != nil {
			t.Fatalf("AcceptClientMutationStart: %v", err)
		}
		assertInput(t, sess.clientMutations.snapshot().PendingExecutions["canonical-start"].Input)
	})
	t.Run("steer", func(t *testing.T) {
		sess := newQueuePersistTestSession(t, t.TempDir())
		defer sess.Close()
		setTestClientMutationActiveTurn(t, sess, "turn_1")
		if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "canonical-steer", ExpectedTurnID: "turn_1", Input: input}); err != nil {
			t.Fatalf("AcceptClientMutationSteer: %v", err)
		}
		assertInput(t, sess.clientMutations.snapshot().PendingExecutions["canonical-steer"].Input)
	})
	t.Run("queue", func(t *testing.T) {
		sess := newQueuePersistTestSession(t, t.TempDir())
		defer sess.Close()
		setTestClientMutationActiveTurn(t, sess, "turn_1")
		if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "canonical-queue", ExpectedTurnID: "turn_1", Input: input}); err != nil {
			t.Fatalf("AcceptClientMutationQueue: %v", err)
		}
		assertInput(t, sess.clientMutations.snapshot().InputQueue[0].Input)
	})
}

func TestClientMutation_QueuePublicationRejectsOlderRevision(t *testing.T) {
	sess := newTestSession(t)
	if err := sess.Enqueue(context.Background(), "durable"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.queueEventsMu.Lock()
	sess.publishedQueueRevision = 2
	sess.mu.Lock()
	sess.inputQueue = []queuedInput{{ID: "newer", Text: "newer projection"}}
	sess.mu.Unlock()
	sess.queueEventsMu.Unlock()

	sess.reflectDurableInputQueue()
	if got := sess.QueueTexts(); len(got) != 1 || got[0] != "newer projection" {
		t.Fatalf("older revision clobbered newer projection: %#v", got)
	}
}

func TestClientMutation_QueueRejectsStaleTurnDurably(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-current")
	params := appwire.TurnQueueParams{
		ClientMutationID: "mutation-stale",
		ExpectedTurnID:   "turn-stale",
		Input:            []appwire.InputItem{{Type: "text", Text: "must not queue"}},
	}

	_, firstErr := sess.clientMutationQueue(params)
	assertClientMutationConflict(t, firstErr)

	snapshot := sess.clientMutations.snapshot()
	record, ok := snapshot.Journal[params.ClientMutationID]
	if !ok {
		t.Fatal("stale rejection was not recorded durably")
	}
	if record.OperationState != clientMutationOperationRejected {
		t.Fatalf("operation state = %q, want rejected", record.OperationState)
	}
	if len(snapshot.InputQueue) != 0 || snapshot.QueueRevision != 0 {
		t.Fatalf("stale rejection changed queue: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
	}

	_, replayErr := sess.clientMutationQueue(params)
	assertClientMutationConflict(t, replayErr)
	if got := sess.clientMutations.snapshot(); len(got.InputQueue) != 0 || got.QueueRevision != 0 {
		t.Fatalf("replayed rejection changed queue: depth=%d revision=%d", len(got.InputQueue), got.QueueRevision)
	}
}

func TestClientMutation_QueueRejectedImagePayloadIsCompactedAndReplayable(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-current")
	params := appwire.TurnQueueParams{
		ClientMutationID: "rejected-image",
		ExpectedTurnID:   "turn-stale",
		Input: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("large-rejected-image-payload"),
		}},
	}
	if _, err := sess.clientMutationQueue(params); err == nil {
		t.Fatal("stale image queue unexpectedly succeeded")
	}
	record := sess.clientMutations.snapshot().Journal[params.ClientMutationID]
	if len(record.Payload) != 0 || record.PayloadHash == "" || record.Rejection == nil {
		t.Fatalf("rejected compacted record payload=%d hash=%q rejection=%#v", len(record.Payload), record.PayloadHash, record.Rejection)
	}
	if _, err := sess.clientMutationQueue(params); err == nil {
		t.Fatal("identical rejected replay unexpectedly succeeded")
	}
	mismatch := params
	mismatch.Input[0].Data = []byte("different")
	if _, err := sess.clientMutationQueue(mismatch); !errors.Is(err, errClientMutationMismatch) {
		t.Fatalf("rejected mismatch error = %v, want %v", err, errClientMutationMismatch)
	}
}

func TestClientMutation_QueueReplayDoesNotDuplicateEffect(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	params := appwire.TurnQueueParams{
		ClientMutationID: "mutation-replay",
		ExpectedTurnID:   "turn-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "once"}},
	}

	first, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("first queue: %v", err)
	}
	replayed, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("replayed queue: %v", err)
	}
	if first.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("first disposition = %q, want applied", first.Receipt.Disposition)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay disposition = %q, want replayed", replayed.Receipt.Disposition)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.QueueRevision != 1 {
		t.Fatalf("replay duplicated effect: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
}

func TestClientMutation_BudgetSerializesConcurrentFinalSlot(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{
		MaxTurns:         1,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	setTestClientMutationActiveTurn(t, sess, "turn-1")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"mutation-a", "mutation-b"} {
		wg.Go(func() {
			<-start
			_, err := sess.clientMutationQueue(appwire.TurnQueueParams{
				ClientMutationID: id,
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: id}},
			})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	var accepted, rejected int
	for err := range errs {
		if err == nil {
			accepted++
			continue
		}
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
			t.Fatalf("queue error = %v, want stored conflict", err)
		}
		rejected++
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 1 and 1", accepted, rejected)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || reservedClientMutationTurns(&snapshot) != 1 {
		t.Fatalf("durable final-slot state: depth=%d reserved=%d", len(snapshot.InputQueue), reservedClientMutationTurns(&snapshot))
	}
	for id, record := range snapshot.Journal {
		if record.OperationState != clientMutationOperationRejected {
			continue
		}
		_, replayErr := sess.clientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: id,
			ExpectedTurnID:   "turn-1",
			Input:            []appwire.InputItem{{Type: "text", Text: id}},
		})
		var firstShape appwire.WireError
		if !errors.As(replayErr, &firstShape) || firstShape.Code != appwire.CodeConflict {
			t.Fatalf("replayed budget rejection = %v, want stored conflict", replayErr)
		}
	}
	if len(sess.clientMutations.owners) != 0 {
		t.Fatalf("owners after terminal outcomes = %d, want 0", len(sess.clientMutations.owners))
	}
}

func TestClientMutation_BudgetSerializesDirectTurnAgainstQueuedFinalSlot(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{
		MaxTurns:         1,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	start := make(chan struct{})
	results := make(chan error, 2)

	go func() {
		<-start
		results <- sess.claimDirectClientMutationTurn(0)
	}()
	go func() {
		<-start
		_, err := sess.clientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: "queue-final-slot",
			ExpectedTurnID:   "turn-1",
			Input:            []appwire.InputItem{{Type: "text", Text: "queued"}},
		})
		results <- err
	}()
	close(start)

	var accepted, rejected int
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want exactly one final-slot winner", accepted, rejected)
	}
	snapshot := sess.clientMutations.snapshot()
	if got := snapshot.AcceptedTurns + reservedClientMutationTurns(&snapshot); got != 1 {
		t.Fatalf("durable turn allocation = %d, want 1", got)
	}
}

func TestClientMutation_CancelPublicPathReleasesDurableReservation(t *testing.T) {
	sess := newTestSession(t)
	if err := sess.Enqueue(context.Background(), "cancel me"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id := sess.QueueIDs()[0]

	removed, images, err := sess.CancelQueued(context.Background(), 0, id)
	if err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	if removed != "cancel me" || images != 0 {
		t.Fatalf("removed=(%q,%d), want (cancel me,0)", removed, images)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 0 || snapshot.QueueRevision != 2 {
		t.Fatalf("durable cancel state: depth=%d revision=%d, want 0 and 2", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
	if got := reservedClientMutationTurns(&snapshot); got != 0 {
		t.Fatalf("reserved turns after cancel = %d, want 0", got)
	}
}

func TestClientMutation_DrainPublicPathReleasesDurableReservations(t *testing.T) {
	sess := newTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	markProcessing(sess)

	if err := sess.DrainAsSteer(context.Background()); err != nil {
		t.Fatalf("DrainAsSteer: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 0 || snapshot.QueueRevision != 3 {
		t.Fatalf("durable drain state: depth=%d revision=%d, want 0 and 3", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
	if got := reservedClientMutationTurns(&snapshot); got != 0 {
		t.Fatalf("reserved turns after drain = %d, want 0", got)
	}
	if len(snapshot.PendingExecutions) != 1 {
		t.Fatalf("durable steering executions = %d, want 1", len(snapshot.PendingExecutions))
	}
}

func TestClientMutation_PromotePublicPathReleasesDurableReservation(t *testing.T) {
	sess := newTestSession(t)
	if err := sess.Enqueue(context.Background(), "promote me"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id := sess.QueueIDs()[0]
	markProcessing(sess)

	if err := sess.PromoteQueuedAsSteer(context.Background(), 0, id); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 0 || snapshot.QueueRevision != 2 {
		t.Fatalf("durable promote state: depth=%d revision=%d, want 0 and 2", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
	if got := reservedClientMutationTurns(&snapshot); got != 0 {
		t.Fatalf("reserved turns after promote = %d, want 0", got)
	}
	if len(snapshot.PendingExecutions) != 1 {
		t.Fatalf("durable steering executions = %d, want 1", len(snapshot.PendingExecutions))
	}
}

func TestClientMutation_SteerPublicPathUsesDurableAuthority(t *testing.T) {
	sess := newTestSession(t)
	markProcessing(sess)

	sess.SteerFromUser("durable steer")

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.PendingExecutions) != 1 {
		t.Fatalf("durable steering executions = %d, want 1", len(snapshot.PendingExecutions))
	}
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method != "turn/steer" || len(pending.Input) != 1 || pending.Input[0].Text != "durable steer" {
			t.Fatalf("pending steering = %#v", pending)
		}
	}
}

func TestClientMutation_DrainRejectsStaleQueueRevisionDurably(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	if err := sess.Enqueue(context.Background(), "queued"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	_, err := sess.clientMutationDrain(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "drain-stale",
		ExpectedTurnID:        "turn-1",
		ExpectedQueueRevision: 0,
	})
	assertClientMutationConflict(t, err)
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.QueueRevision != 1 {
		t.Fatalf("stale drain changed queue: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
	if snapshot.Journal["drain-stale"].OperationState != clientMutationOperationRejected {
		t.Fatalf("stale drain state = %q, want rejected", snapshot.Journal["drain-stale"].OperationState)
	}
}

func TestClientMutation_DrainPreservesMessageBoundaries(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	revision := sess.clientMutations.snapshot().QueueRevision

	_, err := sess.clientMutationDrain(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "drain-boundaries",
		ExpectedTurnID:        "turn-1",
		ExpectedQueueRevision: revision,
		Input:                 []appwire.InputItem{{Type: "text", Text: "charlie"}},
	})
	if err != nil {
		t.Fatalf("clientMutationDrain: %v", err)
	}
	pending := sess.clientMutations.snapshot().PendingExecutions["drain-boundaries"]
	queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
	if queued.Text != "alpha\n\nbravo\n\ncharlie" {
		t.Fatalf("drained text = %q, want blank-line message boundaries", queued.Text)
	}
}

func TestClientMutation_PromoteRejectsShiftedEntryDurably(t *testing.T) {
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	staleID := sess.QueueIDs()[0]
	if _, _, err := sess.CancelQueued(context.Background(), 0, staleID); err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}

	_, err := sess.clientMutationPromote(appwire.TurnPromoteQueuedAsSteerParams{
		Index:            0,
		ClientMutationID: "promote-stale",
		ExpectedTurnID:   "turn-1",
		ExpectedEntryID:  staleID,
	})
	assertClientMutationConflict(t, err)
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || queuedInputFromClientMutation(snapshot.InputQueue[0]).Text != "bravo" {
		t.Fatalf("shifted promote changed queue: %#v", snapshot.InputQueue)
	}
}

func TestClientMutation_QueueReplayReportsRemovedAfterTransform(t *testing.T) {
	for _, transform := range []string{"cancel", "promote", "drain"} {
		t.Run(transform, func(t *testing.T) {
			sess := newTestSession(t)
			setTestClientMutationActiveTurn(t, sess, "turn-1")
			queueParams := appwire.TurnQueueParams{
				ClientMutationID: "queue-source-" + transform,
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "source payload"}},
			}
			queued, err := sess.clientMutationQueue(queueParams)
			if err != nil {
				t.Fatalf("clientMutationQueue: %v", err)
			}
			entryID := queued.Receipt.QueueEntryIDs[0]

			switch transform {
			case "cancel":
				_, err = sess.clientMutationCancel(appwire.TurnCancelQueuedParams{
					Index:            0,
					ClientMutationID: "cancel-transform",
					ExpectedEntryID:  entryID,
				})
			case "promote":
				_, err = sess.clientMutationPromote(appwire.TurnPromoteQueuedAsSteerParams{
					Index:            0,
					ClientMutationID: "promote-transform",
					ExpectedTurnID:   "turn-1",
					ExpectedEntryID:  entryID,
				})
			case "drain":
				_, err = sess.clientMutationDrain(appwire.TurnDrainAsSteerParams{
					ClientMutationID:      "drain-transform",
					ExpectedTurnID:        "turn-1",
					ExpectedQueueRevision: 1,
				})
			}
			if err != nil {
				t.Fatalf("%s transform: %v", transform, err)
			}

			replayed, err := sess.clientMutationQueue(queueParams)
			if err != nil {
				t.Fatalf("replay source queue: %v", err)
			}
			if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
				replayed.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
				t.Fatalf("source replay receipt = %#v, want replayed/removed", replayed.Receipt)
			}
			snapshot := sess.clientMutations.snapshot()
			if len(snapshot.InputQueue) != 0 || snapshot.QueueRevision != 2 {
				t.Fatalf("source replay changed queue: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
			}
			if transform != "cancel" {
				pending, ok := snapshot.PendingExecutions[transform+"-transform"]
				if !ok || len(pending.Input) == 0 || pending.Input[0].Text != "source payload" {
					t.Fatalf("resulting steering does not own source input: %#v, ok=%v", pending, ok)
				}
			}
		})
	}
}

func TestClientMutation_PromoteSerializerSpansValidationAndEffect(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	firstID := sess.QueueIDs()[0]

	checkedSerializer := false
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		checkedSerializer = true
		if sess.clientMutations.mu.TryLock() {
			sess.clientMutations.mu.Unlock()
			return errors.New("mutation serializer was released before effect commit")
		}
		return nil
	}

	if _, err := sess.clientMutationPromote(appwire.TurnPromoteQueuedAsSteerParams{
		Index:            0,
		ClientMutationID: "promote-first",
		ExpectedTurnID:   "turn-1",
		ExpectedEntryID:  firstID,
	}); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if !checkedSerializer {
		t.Fatal("effect-commit serializer seam was not reached")
	}
	_, secondErr := sess.clientMutationPromote(appwire.TurnPromoteQueuedAsSteerParams{
		Index:            0,
		ClientMutationID: "promote-second",
		ExpectedTurnID:   "turn-1",
		ExpectedEntryID:  firstID,
	})
	assertClientMutationConflict(t, secondErr)
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || queuedInputFromClientMutation(snapshot.InputQueue[0]).Text != "bravo" {
		t.Fatalf("serialized promotes changed shifted entry: %#v", snapshot.InputQueue)
	}
}

func TestClientMutation_QueueCrashBoundariesDoNotDuplicateEffect(t *testing.T) {
	tests := []struct {
		name       string
		setFault   func(*clientMutationFaults, func() error)
		firstDepth int
	}{
		{
			name: "after reservation",
			setFault: func(faults *clientMutationFaults, fail func() error) {
				faults.AfterReservation = fail
			},
		},
		{
			name: "before effect rename",
			setFault: func(faults *clientMutationFaults, fail func() error) {
				faults.BeforeEffectSnapshotRename = fail
			},
		},
		{
			name:       "after effect rename",
			firstDepth: 1,
			setFault: func(faults *clientMutationFaults, fail func() error) {
				faults.AfterEffectSnapshotRename = fail
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()
			setTestClientMutationActiveTurn(t, sess, "turn-1")
			var once sync.Once
			tt.setFault(&sess.clientMutations.faults, func() error {
				var err error
				once.Do(func() { err = fmt.Errorf("injected %s crash", tt.name) })
				return err
			})
			params := appwire.TurnQueueParams{
				ClientMutationID: "queue-crash",
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "once"}},
			}

			if _, err := sess.clientMutationQueue(params); err == nil {
				t.Fatal("first queue unexpectedly succeeded")
			}
			if depth := len(sess.clientMutations.snapshot().InputQueue); depth != tt.firstDepth {
				t.Fatalf("depth after injected crash = %d, want %d", depth, tt.firstDepth)
			}
			if _, err := sess.clientMutationQueue(params); err != nil {
				t.Fatalf("retry queue: %v", err)
			}
			snapshot := sess.clientMutations.snapshot()
			if len(snapshot.InputQueue) != 1 || snapshot.QueueRevision != 1 {
				t.Fatalf("retry duplicated effect: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
			}
		})
	}
}

func TestClientMutation_QueueRecoveryRunsAfterReservationSeamOnTakeover(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	calls := 0
	sess.clientMutations.faults.AfterReservation = func() error {
		calls++
		if calls <= 2 {
			return fmt.Errorf("injected reservation crash %d", calls)
		}
		return nil
	}
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-takeover-seam",
		ExpectedTurnID:   "turn-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "once"}},
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := sess.clientMutationQueue(params); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("third attempt: %v", err)
	}
	if calls != 3 {
		t.Fatalf("AfterReservation calls = %d, want first attempt plus both takeovers", calls)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.QueueRevision != 1 {
		t.Fatalf("takeover duplicated effect: depth=%d revision=%d", len(snapshot.InputQueue), snapshot.QueueRevision)
	}
}

func TestClientMutation_TransformTakeoverTargetsReservedEntryIDs(t *testing.T) {
	for _, transform := range []string{"drain", "promote", "cancel"} {
		t.Run(transform, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()
			setTestClientMutationActiveTurn(t, sess, "turn-1")
			alpha := appwire.TurnQueueParams{
				ClientMutationID: "alpha-" + transform,
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "alpha"}},
			}
			bravo := appwire.TurnQueueParams{
				ClientMutationID: "bravo-" + transform,
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "bravo"}},
			}
			alphaResponse, err := sess.clientMutationQueue(alpha)
			if err != nil {
				t.Fatalf("queue alpha: %v", err)
			}
			bravoResponse, err := sess.clientMutationQueue(bravo)
			if err != nil {
				t.Fatalf("queue bravo: %v", err)
			}

			failed := false
			sess.clientMutations.faults.AfterReservation = func() error {
				if !failed {
					failed = true
					return errors.New("crash after transform reservation")
				}
				return nil
			}
			var retry func() error
			switch transform {
			case "drain":
				params := appwire.TurnDrainAsSteerParams{
					ClientMutationID:      "drain-reserved",
					ExpectedTurnID:        "turn-1",
					ExpectedQueueRevision: 2,
				}
				retry = func() error {
					_, err := sess.clientMutationDrain(params)
					return err
				}
			case "promote":
				params := appwire.TurnPromoteQueuedAsSteerParams{
					Index:            1,
					ClientMutationID: "promote-reserved",
					ExpectedTurnID:   "turn-1",
					ExpectedEntryID:  bravoResponse.Receipt.QueueEntryIDs[0],
				}
				retry = func() error {
					_, err := sess.clientMutationPromote(params)
					return err
				}
			case "cancel":
				params := appwire.TurnCancelQueuedParams{
					Index:            1,
					ClientMutationID: "cancel-reserved",
					ExpectedEntryID:  bravoResponse.Receipt.QueueEntryIDs[0],
				}
				retry = func() error {
					_, err := sess.clientMutationCancel(params)
					return err
				}
			}
			if err := retry(); err == nil {
				t.Fatal("first transform unexpectedly succeeded")
			}

			if transform == "drain" {
				if _, err := sess.clientMutationCancel(appwire.TurnCancelQueuedParams{
					Index:            0,
					ClientMutationID: "overlap-cancel-drain",
					ExpectedEntryID:  alphaResponse.Receipt.QueueEntryIDs[0],
				}); err == nil {
					t.Fatal("overlapping cancel took drain-reserved entry")
				}
				if popped := sess.popQueueHead(); popped.ClientMutationID != "" {
					t.Fatalf("pop claimed drain-reserved head: %#v", popped)
				}
				if _, err := sess.clientMutationQueue(appwire.TurnQueueParams{
					ClientMutationID: "charlie-drain",
					ExpectedTurnID:   "turn-1",
					Input:            []appwire.InputItem{{Type: "text", Text: "charlie"}},
				}); err != nil {
					t.Fatalf("intervening queue: %v", err)
				}
			} else {
				if _, err := sess.clientMutationCancel(appwire.TurnCancelQueuedParams{
					Index:            1,
					ClientMutationID: "overlap-cancel-" + transform,
					ExpectedEntryID:  bravoResponse.Receipt.QueueEntryIDs[0],
				}); err == nil {
					t.Fatal("overlapping cancel took reserved target")
				}
				if _, err := sess.clientMutationCancel(appwire.TurnCancelQueuedParams{
					Index:            0,
					ClientMutationID: "intervening-cancel-" + transform,
					ExpectedEntryID:  alphaResponse.Receipt.QueueEntryIDs[0],
				}); err != nil {
					t.Fatalf("intervening cancel: %v", err)
				}
				if popped := sess.popQueueHead(); popped.ClientMutationID != "" {
					t.Fatalf("pop claimed reserved shifted head: %#v", popped)
				}
				if _, err := sess.clientMutationQueue(appwire.TurnQueueParams{
					ClientMutationID: "charlie-" + transform,
					ExpectedTurnID:   "turn-1",
					Input:            []appwire.InputItem{{Type: "text", Text: "charlie"}},
				}); err != nil {
					t.Fatalf("intervening queue: %v", err)
				}
			}
			if err := retry(); err != nil {
				t.Fatalf("transform takeover: %v", err)
			}

			snapshot := sess.clientMutations.snapshot()
			if len(snapshot.InputQueue) != 1 || queuedInputFromClientMutation(snapshot.InputQueue[0]).Text != "charlie" {
				t.Fatalf("takeover targeted shifted/new entry: %#v", snapshot.InputQueue)
			}
			if transform != "cancel" {
				pending := snapshot.PendingExecutions[transform+"-reserved"]
				steered := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
				want := "bravo"
				if transform == "drain" {
					want = "alpha\n\nbravo"
				}
				if steered.Text != want {
					t.Fatalf("takeover steering text = %q, want %q", steered.Text, want)
				}
			}
		})
	}
}

func TestClientMutation_QueueClaimAndTranscriptIncorporationRetainsRunnableIdentity(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-claim",
		Input:            []appwire.InputItem{{Type: "text", Text: "run me"}},
	}
	response, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}

	queued := sess.popQueueHead()
	if queued.ClientMutationID != params.ClientMutationID || queued.StableTurnID == "" {
		t.Fatalf("claimed identity = (%q,%q)", queued.ClientMutationID, queued.StableTurnID)
	}
	claimed := sess.clientMutations.snapshot()
	if claimed.QueueRevision != 2 || claimed.PendingExecutions[params.ClientMutationID].ExecutionState != "claimed" {
		t.Fatalf("claimed snapshot: revision=%d pending=%#v", claimed.QueueRevision, claimed.PendingExecutions[params.ClientMutationID])
	}
	if response.Receipt.QueueEntryIDs[0] != queued.ID {
		t.Fatalf("claimed queue ID = %q, receipt = %#v", queued.ID, response.Receipt.QueueEntryIDs)
	}

	ctx := withQueuedClientMutation(context.Background(), queued)
	if err := sess.acceptUserInput(ctx, queued.Text, queued.Images, nil, false); err != nil {
		t.Fatalf("acceptUserInput: %v", err)
	}
	incorporated := sess.clientMutations.snapshot()
	pending, ok := incorporated.PendingExecutions[params.ClientMutationID]
	if !ok || pending.ExecutionState != "incorporated" || pending.TurnID != queued.StableTurnID {
		t.Fatalf("incorporated pending execution = %#v, ok=%v", pending, ok)
	}
	record := incorporated.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationApplied || record.ExecutionState != "incorporated" {
		t.Fatalf("incorporated record = (%q,%q), want applied/incorporated", record.OperationState, record.ExecutionState)
	}
	last := sess.history[len(sess.history)-1]
	if last.ClientMutationID != params.ClientMutationID || last.StableTurnID != queued.StableTurnID {
		t.Fatalf("transcript identity = (%q,%q)", last.ClientMutationID, last.StableTurnID)
	}
	assertUserInputEventIdentity(t, sess, params.ClientMutationID, queued.StableTurnID)
}

// TestClientMutation_QueueReplayStaysPendingWhileClaimed pins the contract for
// the window between claim and transcript incorporation: popQueueHead makes
// the input durable and claimed, but no transcript item exists for it yet, so
// nothing a client can read may describe it as visible. A retry of the same
// client mutation ID inside that window must still see `pending` -- only
// markClaimedUserTranscriptIncorporated (which runs after the durable
// transcript append) may advance it to `reflected`. Reporting `reflected` here
// would tell the browser its optimistic copy is safe to drop
// (mutationOutboxIndexedDB.ts:172 retains only on "pending") while nothing had
// replaced it.
func TestClientMutation_QueueReplayStaysPendingWhileClaimed(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-claimed-reflection",
		Input:            []appwire.InputItem{{Type: "text", Text: "run me"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	claimed := sess.popQueueHead()
	if claimed.ClientMutationID != params.ClientMutationID {
		t.Fatalf("claimed mutation ID = %q, want %q", claimed.ClientMutationID, params.ClientMutationID)
	}

	replayed, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("replay claimed queue mutation: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("claimed replay receipt = %#v, want replayed/pending", replayed.Receipt)
	}
	snapshot := sess.clientMutations.snapshot()
	if got := snapshot.PendingExecutions[params.ClientMutationID].ProjectionState; got != appwire.MutationProjectionPending {
		t.Fatalf("claimed pending projection = %q, want pending", got)
	}
}

func TestClientMutation_CompletedQueueReplayStaysReflectedByTranscript(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-terminal-reflection",
		Input:            []appwire.InputItem{{Type: "text", Text: "complete me"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	queued := sess.popQueueHead()
	if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), queued), queued.Text, nil, nil, false); err != nil {
		t.Fatalf("acceptUserInput: %v", err)
	}
	if err := sess.completeClientMutationTurn(params.ClientMutationID); err != nil {
		t.Fatalf("completeClientMutationTurn: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	record := snapshot.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "terminal" ||
		record.ProjectionState != appwire.MutationProjectionReflected ||
		len(record.Payload) != 0 {
		t.Fatalf("completed queue record = %#v, want compact terminal reflected tombstone", record)
	}
	if _, ok := snapshot.PendingExecutions[params.ClientMutationID]; ok {
		t.Fatal("completed queue mutation remained pending")
	}
	replayed, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("replay completed queue mutation: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("completed queue replay = %#v, want replayed/reflected", replayed.Receipt)
	}
}

func TestClientMutation_QueueAppendFailureReturnsSameIdentityRunnable(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-append-failure",
		Input:            []appwire.InputItem{{Type: "text", Text: "retry me"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	queued := sess.popQueueHead()
	sess.clientMutationTranscriptAppend = func(schema.Turn) error {
		return errors.New("injected transcript append failure")
	}

	ctx := withQueuedClientMutation(context.Background(), queued)
	if err := sess.acceptUserInput(ctx, queued.Text, queued.Images, nil, false); err == nil {
		t.Fatal("acceptUserInput unexpectedly succeeded")
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != queued.ClientMutationID ||
		snapshot.InputQueue[0].ID != queued.ID {
		t.Fatalf("runnable queue identity = %#v, want original", snapshot.InputQueue)
	}
	if snapshot.Journal[queued.ClientMutationID].StableTurnID != queued.StableTurnID {
		t.Fatalf("stable turn changed after append failure: %q -> %q", queued.StableTurnID, snapshot.Journal[queued.ClientMutationID].StableTurnID)
	}
	if snapshot.QueueRevision != 3 {
		t.Fatalf("queue revision = %d, want enqueue+claim+return = 3", snapshot.QueueRevision)
	}
	// pushQueueHead moves this mutation BACKWARD to queued state -- no
	// transcript item describes it, so a retry inside this window must still
	// see pending, the same contract as a fresh claim.
	if got := snapshot.Journal[queued.ClientMutationID].ProjectionState; got != appwire.MutationProjectionPending {
		t.Fatalf("returned-to-queue projection state = %q, want pending", got)
	}
}

func TestClientMutation_SteerClaimFinalizesOnlyAfterDurableAppend(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnSteerParams{
		ClientMutationID: "steer-claim",
		Input:            []appwire.InputItem{{Type: "text", Text: "look here"}},
	}
	response, err := sess.clientMutationSteer(params)
	if err != nil {
		t.Fatalf("clientMutationSteer: %v", err)
	}
	if pending, ok := projectedClientMutation(sess, params.ClientMutationID); !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("accepted steering projection = %#v, ok=%v", pending, ok)
	}
	msg, ok := sess.popSteeringHead()
	if !ok {
		t.Fatal("popSteeringHead returned empty")
	}
	claimed := sess.clientMutations.snapshot()
	if claimed.PendingExecutions[params.ClientMutationID].ExecutionState != "claimed" {
		t.Fatalf("steering was not claimed: %#v", claimed.PendingExecutions[params.ClientMutationID])
	}
	if pending, ok := projectedClientMutation(sess, params.ClientMutationID); !ok || pending.ExecutionState != "claimed" {
		t.Fatalf("claimed steering projection = %#v, ok=%v", pending, ok)
	}
	if msg.StableTurnID != response.Receipt.TurnID {
		t.Fatalf("steering stable turn = %q, receipt = %q", msg.StableTurnID, response.Receipt.TurnID)
	}

	sess.consumeSteeringMessage(msg)
	incorporated := sess.clientMutations.snapshot()
	if _, ok := incorporated.PendingExecutions[params.ClientMutationID]; ok {
		t.Fatal("incorporated steering remained pending")
	}
	record := incorporated.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal || record.ExecutionState != "incorporated" {
		t.Fatalf("steering terminal record = (%q,%q)", record.OperationState, record.ExecutionState)
	}
	last := sess.history[len(sess.history)-1]
	if last.ClientMutationID != params.ClientMutationID || last.StableTurnID != response.Receipt.TurnID {
		t.Fatalf("steering transcript identity = (%q,%q)", last.ClientMutationID, last.StableTurnID)
	}
}

func projectedClientMutation(sess *Session, clientMutationID string) (appwire.PendingMutation, bool) {
	_, pending := sess.ClientMutationProjection()
	for _, mutation := range pending {
		if mutation.ClientMutationID == clientMutationID {
			return mutation, true
		}
	}
	return appwire.PendingMutation{}, false
}

func TestClientMutation_SteerAppendFailureReturnsSameIdentityRunnable(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnSteerParams{
		ClientMutationID: "steer-append-failure",
		Input:            []appwire.InputItem{{Type: "text", Text: "retry steer"}},
	}
	response, err := sess.clientMutationSteer(params)
	if err != nil {
		t.Fatalf("clientMutationSteer: %v", err)
	}
	msg, ok := sess.popSteeringHead()
	if !ok {
		t.Fatal("popSteeringHead returned empty")
	}
	sess.reflectDurableClientSteering()
	if got := sess.SteeringQueueSnapshot(); len(got) != 0 {
		t.Fatalf("claimed steering was reprojected before append outcome: %#v", got)
	}
	sess.clientMutationTranscriptAppend = func(schema.Turn) error {
		return errors.New("injected transcript append failure")
	}
	sess.consumeSteeringMessage(msg)

	snapshot := sess.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions[params.ClientMutationID]
	if !ok || pending.ExecutionState != "accepted" || pending.TurnID != response.Receipt.TurnID {
		t.Fatalf("runnable steering = %#v, ok=%v", pending, ok)
	}
	steering := sess.SteeringQueueSnapshot()
	if len(steering) != 1 || steering[0].Text != "retry steer" {
		t.Fatalf("runtime steering after append failure = %#v", steering)
	}
}

func TestClientMutation_SteeringClaimRestoreDoesNotConsumeTurnBudget(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnSteerParams{
		ClientMutationID: "steer-claimed-budget-restore",
		Input:            []appwire.InputItem{{Type: "text", Text: "restore steering"}},
	}
	if _, err := sess.clientMutationSteer(params); err != nil {
		t.Fatalf("clientMutationSteer: %v", err)
	}
	if _, ok := sess.popSteeringHead(); !ok {
		t.Fatal("popSteeringHead returned empty")
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.AcceptedTurns = 3
		return nil
	}); err != nil {
		t.Fatalf("seed accepted turns: %v", err)
	}

	sess.restoreDurableClientMutationQueues()
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[params.ClientMutationID]
	if pending.ExecutionState != "accepted" {
		t.Fatalf("restored steering = %#v", pending)
	}
	if snapshot.AcceptedTurns != 3 {
		t.Fatalf("accepted turns after steering restore = %d, want 3", snapshot.AcceptedTurns)
	}
	if _, reserved := snapshot.BudgetReservations[params.ClientMutationID]; reserved {
		t.Fatal("restored steering acquired a user-turn budget reservation")
	}
}

func TestClientMutation_CommunicateEndTurnDurablyConsumesClientSteering(t *testing.T) {
	sess := newTestSession(t)
	clientImage := appwire.InputItem{
		Type:      "image",
		MediaType: "image/png",
		Data:      []byte("client-image"),
		Name:      "client.png",
	}
	params := appwire.TurnSteerParams{
		ClientMutationID: "communicate-steer",
		Input: []appwire.InputItem{
			{Type: "text", Text: "client update"},
			clientImage,
		},
	}
	response, err := sess.clientMutationSteer(params)
	if err != nil {
		t.Fatalf("clientMutationSteer: %v", err)
	}
	daemonImage := ImageAttachment{MediaType: "image/png", Data: []byte("daemon-image"), Name: "daemon.png"}
	if !sess.trySteerWithImages("daemon reminder", []ImageAttachment{daemonImage}) {
		t.Fatal("queue daemon steering")
	}

	communicate := sess.reg.Get(sess.resultToolName())
	if communicate == nil {
		t.Fatal("production communicate tool is not registered")
	}
	raw, err := communicate.Exec(context.Background(), nil, map[string]any{
		"message":  "done",
		"end_turn": true,
	})
	if err != nil {
		t.Fatalf("communicate(end_turn): %v", err)
	}
	var result struct {
		Inbox []string `json:"inbox"`
	}
	if err := json.Unmarshal([]byte(raw.(string)), &result); err != nil {
		t.Fatalf("decode communicate result: %v", err)
	}
	if got, want := result.Inbox, []string{"client update", "daemon reminder"}; !slices.Equal(got, want) {
		t.Fatalf("communicate inbox = %#v, want %#v", got, want)
	}

	snapshot := sess.clientMutations.snapshot()
	record := snapshot.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "incorporated" {
		t.Fatalf("consumed steering record = (%q,%q), want terminal/incorporated",
			record.OperationState, record.ExecutionState)
	}
	if _, ok := snapshot.PendingExecutions[params.ClientMutationID]; ok {
		t.Fatal("communicate-consumed steering remained pending")
	}

	var incorporated int
	for _, turn := range sess.history {
		if turn.ClientMutationID != params.ClientMutationID {
			continue
		}
		incorporated++
		if turn.StableTurnID != response.Receipt.TurnID {
			t.Fatalf("incorporated stable turn = %q, want %q", turn.StableTurnID, response.Receipt.TurnID)
		}
		var images int
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentImage {
				images++
			}
		}
		if images != 1 {
			t.Fatalf("incorporated client image count = %d, want 1", images)
		}
	}
	if incorporated != 1 {
		t.Fatalf("incorporated client steering count = %d, want 1", incorporated)
	}

	sess.reflectDurableClientSteering()
	steering := sess.SteeringQueueSnapshot()
	if len(steering) != 1 ||
		steering[0].Text != "daemon reminder" ||
		len(steering[0].Images) != 1 {
		t.Fatalf("post-reflection steering = %#v, want only deferred daemon image steering", steering)
	}
}

func TestClientMutation_SteeringProducerReplayStaysReflectedAfterTranscriptIncorporation(t *testing.T) {
	for _, operation := range []string{"steer", "drain", "promote"} {
		t.Run(operation, func(t *testing.T) {
			sess := newTestSession(t)
			mutationID := operation + "-incorporated-reflection"
			var replay func() (appwire.MutationReceipt, error)

			switch operation {
			case "steer":
				params := appwire.TurnSteerParams{
					ClientMutationID: mutationID,
					Input:            []appwire.InputItem{{Type: "text", Text: "steer text"}},
				}
				if _, err := sess.clientMutationSteer(params); err != nil {
					t.Fatalf("clientMutationSteer: %v", err)
				}
				replay = func() (appwire.MutationReceipt, error) {
					response, err := sess.clientMutationSteer(params)
					return response.Receipt, err
				}
			case "drain":
				if _, err := sess.clientMutationQueue(appwire.TurnQueueParams{
					ClientMutationID: "drain-source",
					Input:            []appwire.InputItem{{Type: "text", Text: "drain text"}},
				}); err != nil {
					t.Fatalf("queue drain source: %v", err)
				}
				params := appwire.TurnDrainAsSteerParams{
					ClientMutationID:      mutationID,
					ExpectedQueueRevision: sess.clientMutations.snapshot().QueueRevision,
				}
				if _, err := sess.clientMutationDrain(params); err != nil {
					t.Fatalf("clientMutationDrain: %v", err)
				}
				replay = func() (appwire.MutationReceipt, error) {
					response, err := sess.clientMutationDrain(params)
					return response.Receipt, err
				}
			case "promote":
				queued, err := sess.clientMutationQueue(appwire.TurnQueueParams{
					ClientMutationID: "promote-source",
					Input:            []appwire.InputItem{{Type: "text", Text: "promote text"}},
				})
				if err != nil {
					t.Fatalf("queue promote source: %v", err)
				}
				params := appwire.TurnPromoteQueuedAsSteerParams{
					Index:            0,
					ClientMutationID: mutationID,
					ExpectedEntryID:  queued.Receipt.QueueEntryIDs[0],
				}
				if _, err := sess.clientMutationPromote(params); err != nil {
					t.Fatalf("clientMutationPromote: %v", err)
				}
				replay = func() (appwire.MutationReceipt, error) {
					response, err := sess.clientMutationPromote(params)
					return response.Receipt, err
				}
			}

			msg, ok := sess.popSteeringHead()
			if !ok || msg.ClientMutationID != mutationID {
				t.Fatalf("claimed steering = %#v, ok=%v", msg, ok)
			}
			if !sess.consumeSteeringMessage(msg) {
				t.Fatal("consumeSteeringMessage did not durably incorporate steering")
			}

			receipt, err := replay()
			if err != nil {
				t.Fatalf("replay incorporated %s mutation: %v", operation, err)
			}
			if receipt.Disposition != appwire.MutationDispositionReplayed ||
				receipt.ProjectionState != appwire.MutationProjectionReflected {
				t.Fatalf("incorporated %s replay receipt = %#v, want replayed/reflected", operation, receipt)
			}
			record := sess.clientMutations.snapshot().Journal[mutationID]
			if record.OperationState != clientMutationOperationTerminal ||
				record.ExecutionState != "incorporated" ||
				record.ProjectionState != appwire.MutationProjectionReflected ||
				len(record.Payload) != 0 {
				t.Fatalf("incorporated %s record = %#v, want compact terminal reflected tombstone", operation, record)
			}
		})
	}
}

func TestClientMutation_QueueRecoveryUsesFullTranscriptIdentityAfterCompaction(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-compacted-identity",
		Input:            []appwire.InputItem{{Type: "text", Text: "already appended"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	queued := sess.popQueueHead()
	sess.history = []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.Assistant("compacted context"))}
	sess.restoredClientMutationTurns = map[string]string{
		queued.ClientMutationID: queued.StableTurnID,
	}

	sess.restoreDurableClientMutationQueues()
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[queued.ClientMutationID]
	if pending.ExecutionState != "incorporated" || pending.TurnID != queued.StableTurnID {
		t.Fatalf("full-transcript recovery identity = %#v", pending)
	}
	if len(snapshot.InputQueue) != 0 {
		t.Fatalf("compacted history caused duplicate requeue: %#v", snapshot.InputQueue)
	}
}

func TestClientMutation_QueueRestoreFencesPreRestartRevision(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	if err := sess.Enqueue(context.Background(), "accepted"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	preRestartRevision := sess.clientMutations.snapshot().QueueRevision
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	if snapshot.QueueRevision != preRestartRevision+1 {
		t.Fatalf("restored queue revision = %d, want %d", snapshot.QueueRevision, preRestartRevision+1)
	}
	setTestClientMutationActiveTurn(t, restored, "turn-1")
	_, err := restored.clientMutationDrain(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "stale-pre-restart-drain",
		ExpectedTurnID:        "turn-1",
		ExpectedQueueRevision: preRestartRevision,
	})
	assertClientMutationConflict(t, err)
	if got := restored.QueueTexts(); len(got) != 1 || got[0] != "accepted" {
		t.Fatalf("stale pre-restart drain changed queue: %#v", got)
	}
}

func TestClientMutation_EmptyQueueRestoreStillFencesRevision(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	before := sess.clientMutations.snapshot().QueueRevision
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	if got := restored.clientMutations.snapshot().QueueRevision; got != before+1 {
		t.Fatalf("empty restore revision = %d, want %d", got, before+1)
	}
}

func TestClientMutation_CancelCompactsImagePayloadButKeepsHashReplay(t *testing.T) {
	sess := newTestSession(t)
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-image-payload",
		Input: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("large-sensitive-image-bytes"),
			Name:      "capture.png",
		}},
	}
	response, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	if _, err := sess.clientMutationCancel(appwire.TurnCancelQueuedParams{
		Index:            0,
		ClientMutationID: "cancel-image-payload",
		ExpectedEntryID:  response.Receipt.QueueEntryIDs[0],
	}); err != nil {
		t.Fatalf("clientMutationCancel: %v", err)
	}
	record := sess.clientMutations.snapshot().Journal[params.ClientMutationID]
	if len(record.Payload) != 0 || record.PayloadHash == "" || len(record.Result) == 0 {
		t.Fatalf("compacted record payload=%d hash=%q result=%d", len(record.Payload), record.PayloadHash, len(record.Result))
	}
	replayed, err := sess.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("compacted replay receipt = %#v", replayed.Receipt)
	}
	mismatch := params
	mismatch.Input[0].Data = []byte("different-image")
	if _, err := sess.clientMutationQueue(mismatch); !errors.Is(err, errClientMutationMismatch) {
		t.Fatalf("mismatched compacted replay error = %v, want %v", err, errClientMutationMismatch)
	}
}

func TestClientMutation_CancelOperationIsCompactRemovedTombstone(t *testing.T) {
	sess := newTestSession(t)
	queued, err := sess.clientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cancel-operation-source",
		Input: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("image-to-cancel"),
			Name:      "cancel.png",
		}},
	})
	if err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	params := appwire.TurnCancelQueuedParams{
		Index:            0,
		ClientMutationID: "cancel-operation",
		ExpectedEntryID:  queued.Receipt.QueueEntryIDs[0],
	}
	initial, err := sess.clientMutationCancel(params)
	if err != nil {
		t.Fatalf("clientMutationCancel: %v", err)
	}
	if initial.Receipt.Disposition != appwire.MutationDispositionApplied ||
		initial.Receipt.ProjectionState != appwire.MutationProjectionRemoved ||
		initial.RemovedImages != 1 {
		t.Fatalf("initial cancel response = %#v, want applied/removed with one image", initial)
	}

	record := sess.clientMutations.snapshot().Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "canceled" ||
		record.ProjectionState != appwire.MutationProjectionRemoved ||
		len(record.Payload) != 0 ||
		record.PayloadHash == "" ||
		len(record.Result) == 0 {
		t.Fatalf("cancel operation record = %#v, want compact terminal removed tombstone", record)
	}

	replayed, err := sess.clientMutationCancel(params)
	if err != nil {
		t.Fatalf("replay cancel operation: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.ProjectionState != appwire.MutationProjectionRemoved ||
		replayed.RemovedImages != 1 {
		t.Fatalf("replayed cancel response = %#v, want replayed/removed original result", replayed)
	}

	mismatch := params
	mismatch.ExpectedEntryID = "different-entry"
	if _, err := sess.clientMutationCancel(mismatch); !errors.Is(err, errClientMutationMismatch) {
		t.Fatalf("mismatched compacted cancel error = %v, want %v", err, errClientMutationMismatch)
	}
}

func TestClientMutation_QueueRestoreKeepsIncorporatedTurnWithoutDuplicateInput(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-incorporated-restore",
		Input:            []appwire.InputItem{{Type: "text", Text: "resume work"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	queued := sess.popQueueHead()
	if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), queued), queued.Text, nil, nil, false); err != nil {
		t.Fatalf("acceptUserInput: %v", err)
	}
	stableTurnID := queued.StableTurnID
	beforeRestart := sess.clientMutations.snapshot()
	if got := beforeRestart.PendingExecutions[params.ClientMutationID].ProjectionState; got != appwire.MutationProjectionReflected {
		t.Fatalf("incorporated projection before restart = %q, want reflected", got)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions[params.ClientMutationID]
	if !ok || pending.ExecutionState != "incorporated" || pending.TurnID != stableTurnID {
		t.Fatalf("restored runnable turn = %#v, ok=%v", pending, ok)
	}
	if pending.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("restored incorporated projection = %q, want reflected", pending.ProjectionState)
	}
	replayed, err := restored.clientMutationQueue(params)
	if err != nil {
		t.Fatalf("replay incorporated queue mutation: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("incorporated replay receipt = %#v, want replayed/reflected", replayed.Receipt)
	}
	if len(snapshot.InputQueue) != 0 {
		t.Fatalf("already-incorporated input was requeued: %#v", snapshot.InputQueue)
	}
	var matches int
	for _, turn := range restored.history {
		if turn.ClientMutationID == params.ClientMutationID {
			matches++
			if turn.StableTurnID != stableTurnID {
				t.Fatalf("restored stable turn = %q, want %q", turn.StableTurnID, stableTurnID)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("restored transcript identity count = %d, want 1", matches)
	}
	resumed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim restored incorporated queue turn: resumed=%#v ok=%v err=%v", resumed, ok, err)
	}
	if resumed.ClientMutationID != params.ClientMutationID ||
		resumed.StableTurnID != stableTurnID ||
		resumed.Text != "resume work" {
		t.Fatalf("restored queue lifecycle claim = %#v", resumed)
	}
	if err := restored.acceptUserInput(
		withQueuedClientMutation(context.Background(), resumed),
		resumed.Text,
		resumed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("resume incorporated queue turn: %v", err)
	}
	var afterResume int
	for _, turn := range restored.history {
		if turn.ClientMutationID == params.ClientMutationID {
			afterResume++
		}
	}
	if afterResume != 1 {
		t.Fatalf("resumed incorporated queue duplicated transcript: count=%d", afterResume)
	}
}

// TestClientMutation_QueueClaimedWithoutTranscriptRestoresPendingOnRequeue is
// the crash-recovery counterpart to the test above: a queue entry claimed but
// never incorporated before the crash has no transcript item describing it,
// so restoreDurableClientMutationQueues must requeue it as pending -- the
// same not-yet-visible state a fresh queue entry starts in -- not reassert
// the reflected state that only a genuine transcript append earns.
func TestClientMutation_QueueClaimedWithoutTranscriptRestoresPendingOnRequeue(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnQueueParams{
		ClientMutationID: "queue-claimed-recovery",
		Input:            []appwire.InputItem{{Type: "text", Text: "resume claimed queue"}},
	}
	if _, err := sess.clientMutationQueue(params); err != nil {
		t.Fatalf("clientMutationQueue: %v", err)
	}
	claimed := sess.popQueueHead()
	if claimed.ClientMutationID != params.ClientMutationID {
		t.Fatalf("popQueueHead claimed = %#v, want %q", claimed, params.ClientMutationID)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 ||
		snapshot.InputQueue[0].ClientMutationID != params.ClientMutationID ||
		snapshot.InputQueue[0].ID != claimed.ID {
		t.Fatalf("restored requeued entry = %#v, want original claimed entry", snapshot.InputQueue)
	}
	if _, ok := snapshot.PendingExecutions[params.ClientMutationID]; ok {
		t.Fatal("restored requeued mutation remained runnable in PendingExecutions")
	}
	if got := snapshot.Journal[params.ClientMutationID].ProjectionState; got != appwire.MutationProjectionPending {
		t.Fatalf("restored requeued projection = %q, want pending", got)
	}
}

func assertUserInputEventIdentity(t *testing.T, sess *Session, mutationID, stableTurnID string) {
	t.Helper()
	for {
		select {
		case event := <-sess.Events():
			if event.Kind != events.EventUserInput {
				continue
			}
			data, ok := event.Data.(events.UserInputData)
			if !ok {
				t.Fatalf("user input event data = %T", event.Data)
			}
			if data.ClientMutationID != mutationID || data.StableTurnID != stableTurnID {
				t.Fatalf("user input event identity = (%q,%q)", data.ClientMutationID, data.StableTurnID)
			}
			return
		default:
			t.Fatal("user input event was not emitted")
		}
	}
}

func setTestClientMutationActiveTurn(t *testing.T, sess *Session, turnID string) {
	t.Helper()
	sess.clientMutations.mu.Lock()
	sess.clientMutations.state.ActiveTurnID = turnID
	sess.clientMutations.mu.Unlock()
}

func assertClientMutationConflict(t *testing.T, err error) {
	t.Helper()
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want appwire conflict", err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d, want %d", wireErr.Code, appwire.CodeConflict)
	}
}
