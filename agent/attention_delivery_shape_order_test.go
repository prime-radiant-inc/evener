package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// seedQueuedUpdate posts one update from delegateID and returns the delivery
// identity the journal queued for it. The post is a real event, so the pending
// queue holds the same shape production holds.
func seedQueuedUpdate(t *testing.T, c *delegateTreeController, delegateID, message string) string {
	t.Helper()
	c.mu.Lock()
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:         delegatestore.EventDelegateUpdatePosted,
		TS:           c.now(),
		DelegateID:   delegateID,
		UpdatePosted: &delegatestore.UpdatePosted{Generation: 1, Message: message},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("post an update for %s: %v", delegateID, err)
	}
	if len(appended) == 0 {
		t.Fatalf("the update post appended no event")
	}
	return delegatestore.UpdateDeliveryID(delegateID, appended[0].Seq)
}

// TestADeferredUpdateIsJudgedByItsPostersQueue pins that the acknowledgment gate
// answers for both identity shapes.
//
// Deliveries are minted in two shapes: a generation's report
// (<id>/delivery/<generation>) and an update (<id>/update/<seq>). Both are
// filed in the posting delegate's queue. A gate that parses only the first
// shape returns false for an update, which reads as "acknowledged" — so a
// compaction that dropped an update's attention turn would restore it before
// the acknowledgment released the parent's older held report. A parser that
// silently misses a shape is how that happened, so the shape coverage is
// pinned here for both, and an unparseable identity is pinned to withhold
// rather than admit.
func TestADeferredUpdateIsJudgedByItsPostersQueue(t *testing.T) {
	const (
		parentDelegateID = "dlg_update_parent"
		childDelegateID  = "dlg_update_child"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, parentDelegateID, "")
	seedDelegateControllerRunning(t, c, childDelegateID, parentDelegateID)

	updateDeliveryID := seedQueuedUpdate(t, c, childDelegateID, "an update still queued")
	updateID := delegateAttentionID(updateDeliveryID)
	if queued := pendingDeliveryIDs(c, childDelegateID); len(queued) != 1 || queued[0] != updateDeliveryID {
		t.Fatalf("premise broken: the child's queue holds %v, want only %q", queued, updateDeliveryID)
	}

	receiver := &Session{id: "sess-update-receiver", delegateController: c, owningDelegateID: parentDelegateID}
	if !receiver.delegateAttentionReportAwaitsAcknowledgment(updateID) {
		t.Fatalf("the queued update %q reads as acknowledged: the pending-turn rebuild would publish it before the acknowledgment that releases the older held report, so a request presents the pair inverted", updateID)
	}
	// The parser covers both shapes, so the report case still answers.
	reportDeliveryID := delegateDeliveryID("dlg_reporter", 3)
	reportReceiver := &Session{id: "sess-report-receiver", delegateController: c, owningDelegateID: parentDelegateID}
	if reportReceiver.delegateAttentionReportAwaitsAcknowledgment(delegateAttentionID(reportDeliveryID)) {
		t.Fatalf("an identity whose queue holds nothing reported itself as unacknowledged: %q", reportDeliveryID)
	}
	// An identity neither shape can split must withhold, not admit.
	if !receiver.delegateAttentionReportAwaitsAcknowledgment("delegate:dlg_x/unknown/1") {
		t.Fatal("an identity no shape can split read as acknowledged: an unanswerable question must withhold, because publication that waits is recoverable and publication that runs early presents a report whose release is unread")
	}

	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID:           childDelegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: updateDeliveryID},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge %q: %v", updateDeliveryID, err)
	}
	if receiver.delegateAttentionReportAwaitsAcknowledgment(updateID) {
		t.Fatalf("the acknowledged update %q still reads as unacknowledged: a restore would withhold a row whose release floor has advanced", updateID)
	}
}

// TestPublicationLeavesARowAnotherPathAlreadyResolved pins that publication
// re-reads the row's resolution before it writes history.
//
// Acknowledgment makes a report publishable and also makes it eligible for a
// drive, and the drive can resolve the row before the publication callback
// runs. A stop that lands there discards the original and reopens the report
// under a held identity; a drive that accepts it marks the original consumed.
// In both cases the row is complete, and its history belongs to the path that
// resolved it. Publication that inserts anyway re-presents a report the user
// stopped, which is the exact quiet this PR exists to keep.
//
// The ordering is built where production builds it: resolve first, publish
// after. Both branches are exercised, because the caller's own turn is what
// distinguishes them — a stop retry holds the report ID and passes a turn, a
// plain publication passes none and reads the row's turn from the fold.
func TestPublicationLeavesARowAnotherPathAlreadyResolved(t *testing.T) {
	for _, branch := range []struct {
		name     string
		withTurn bool
	}{
		{name: "with the callers turn", withTurn: true},
		{name: "reading the turn from the fold"},
	} {
		t.Run(branch.name, func(t *testing.T) {
			const attentionID = "delegate:delivery-resolved-in-between"
			profile := namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")
			stateDir := realTempDirForTest(t)
			runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))

			deposited, _, err := runtime.appendDelegateAttentionMessageDurablyDeferred(attentionID, llm.User("a report a stop owns"))
			if err != nil {
				t.Fatalf("deposit the report: %v", err)
			}
			// The other path resolves the row before the publication callback
			// runs: the stop's discard and a drive's acceptance both look like
			// this from publication's side.
			if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionDiscarded); err != nil {
				t.Fatalf("resolve the row from the other path: %v", err)
			}

			var turn schema.Turn
			if branch.withTurn {
				turn = deposited
			}
			if err := runtime.publishAcknowledgedDelegateAttentionReport(attentionID, turn); err != nil {
				t.Fatalf("publish: %v", err)
			}

			runtime.mu.Lock()
			defer runtime.mu.Unlock()
			for _, resident := range runtime.history {
				if resident.AttentionID == attentionID {
					t.Fatalf("publication reinserted %q after another path resolved it: a stop's reopen and a drive's acceptance both own that row's history, and presenting it from here re-presents a report the user stopped", attentionID)
				}
			}
		})
	}
}

// TestARecoveredUpdateTriggerKeepsItsArrivalOrder pins that the recovered
// trigger's tail move is keyed on what the row is.
//
// An update and the report it will be followed by can both be pending when a
// drive accepts the update. A crash after the consumed marker but before
// RunStarted makes the update the recovered trigger. Moving it to the tail
// would put it after the report, reversing the delivery queue's order — the
// order a resident release preserves. Only a reopened row, whose turn the
// settle left behind later exchanges, is owed that position.
func TestARecoveredUpdateTriggerKeepsItsArrivalOrder(t *testing.T) {
	const (
		parentDelegateID = "dlg_order_parent"
		childDelegateID  = "dlg_order_child"
		sessionID        = "sess-order-receiver"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, parentDelegateID, "")
	seedDelegateControllerRunning(t, c, childDelegateID, parentDelegateID)
	updateDeliveryID := seedQueuedUpdate(t, c, childDelegateID, "the update that came first")
	updateID := delegateAttentionID(updateDeliveryID)
	reportID := delegateAttentionID(delegateDeliveryID(childDelegateID, 1))

	path := transcriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	updateTurn := schema.NewTurn(schema.TurnSteering, llm.User("the update that came first"))
	updateTurn.AttentionID = updateID
	reportTurn := schema.NewTurn(schema.TurnSteering, llm.User("the terminal report that came second"))
	reportTurn.AttentionID = reportID
	for _, turn := range []schema.Turn{updateTurn, reportTurn} {
		if err := writer.AppendDurable(turn); err != nil {
			t.Fatalf("append %q: %v", turn.AttentionID, err)
		}
	}

	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c, owningDelegateID: parentDelegateID}
	runtime.attachTranscript(writer, nil)
	runtime.mu.Lock()
	runtime.history = append(runtime.history, updateTurn, reportTurn)
	runtime.mu.Unlock()

	if err := runtime.restoreAcceptedTrigger(updateID, []schema.Turn{updateTurn, reportTurn}); err != nil {
		t.Fatalf("restore the recovered update: %v", err)
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var order []string
	for _, turn := range runtime.history {
		if turn.AttentionID == updateID || turn.AttentionID == reportID {
			order = append(order, turn.AttentionID)
		}
	}
	if len(order) != 2 || order[0] != updateID || order[1] != reportID {
		t.Fatalf("the recovered request orders %v, want the update %q before the report %q: moving an ordinary trigger to the tail reverses the delivery queue's order", order, updateID, reportID)
	}
}
