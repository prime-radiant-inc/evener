package agent

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestSendRestoreRebuildsAStoppedRowsQuietBeforeTheAttach pins the rule that the
// shared idle-restore path an explicit send reaches rebuilds a user stop's quiet
// before the restored runtime can be attached.
//
// The controller survives a same-process idle reclaim, so nothing about the
// reclaim journals a release: the stop's quiet lived only in the memory of the
// runtime the reclaim released. A send restores that delegate against the same
// controller and attaches the new runtime. If the send attached without
// deriving the quiet, selection would read an empty hold set for the stopped
// generation and an automatic drive could start a generation that presents the
// report the user stopped — the same harm the resident hold exists to prevent.
func TestSendRestoreRebuildsAStoppedRowsQuietBeforeTheAttach(t *testing.T) {
	t.Parallel()
	root, tree, _ := newRetirementDelegateController(t)
	defer root.Close()
	result := retirementIdleDelegate(t, root)
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatalf("reclaim the delegate runtime: %v", err)
	}
	controller := root.delegateController
	controller.mu.Lock()
	aggregate := controller.durable[result.DelegateID]
	if aggregate == nil {
		controller.mu.Unlock()
		t.Fatal("premise broken: the fixture delegate left no aggregate")
	}
	// The stop settles a reuse of this delegate's next generation as an
	// attention run that ended cancelled: the shape a user's stop leaves, and
	// the one the derivation must rebuild a quiet from.
	generation := aggregate.Generation + 1
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	deliveryID := delegateDeliveryID(result.DelegateID, generation)
	_, err := controller.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: result.DelegateID,
			RunStarted: &delegatestore.RunStarted{Generation: generation, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(10, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: result.DelegateID,
			RunFinished: &delegatestore.RunFinished{
				Generation:  generation,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "stopped", EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  deliveryID,
				Packet:      &stopped,
			},
		},
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID:           result.DelegateID,
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: deliveryID},
		},
	)
	// The reclaim released the runtime that held the stop's quiet in memory, and
	// nothing about a reclaim journals a release.
	released := aggregate.AttentionHoldReleasedGeneration
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("journal the stopped generation: %v", err)
	}
	if released >= generation {
		t.Fatalf("premise broken: the release floor is already %d at generation %d, so no quiet stands to rebuild", released, generation)
	}

	// The stop's own report: the run consumed it, then the settle reopened it,
	// and nothing has presented it since.
	const sourceID = "delegate:stopped-run-report"
	reopenedID := delegateAttentionReopenID(sourceID, generation)
	path := transcriptPath(tree.stateDir, result.ChildSessionID)
	appendColdAttentionRow(t, path, result.ChildSessionID, sourceID, acceptedTriggerText)
	appendColdConsumedClaim(t, path, result.ChildSessionID, sourceID, generation)
	appendColdAttentionRow(t, path, result.ChildSessionID, reopenedID, acceptedTriggerText)

	// The same controller's wake state survived the reclaim, and no arm has
	// noted this row: the stop's quiet is the only thing keeping it out.
	controller.mu.Lock()
	noted := false
	if state := controller.attention[result.DelegateID]; state != nil {
		_, noted = state.wakeIDs[reopenedID]
	}
	controller.mu.Unlock()
	if noted {
		t.Fatal("premise broken: the row's wake is already noted, so it presents on its own and no quiet is owed")
	}

	started, _, err := tree.idleDelegateRestoreCommit(result.DelegateID)
	if err != nil {
		t.Fatalf("idle restore commit: %v", err)
	}
	candidate, restored, finish, err := (delegateRuntime{owner: root}).restoreIdleForSend(started)
	if err != nil {
		finish(nil, err)
		t.Fatalf("restoreIdleForSend: %v", err)
	}
	if !restored {
		finish(candidate, nil)
		t.Fatal("premise broken: the send reused a resident runtime instead of restoring a cold one")
	}
	defer finish(candidate, nil)

	candidate.sess.attentionMu.Lock()
	held := candidate.sess.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
	stamp := candidate.sess.attentionAutomaticDriveHolds[reopenedID]
	candidate.sess.attentionMu.Unlock()
	if !held {
		t.Fatalf("the send's restore attached a runtime with no hold for %q: selection reads the automatic list, so an automatic drive could start a generation that presents the report the user stopped", reopenedID)
	}
	if stamp != generation {
		t.Fatalf("the restored hold carries stamp %d, want the stop's own generation %d", stamp, generation)
	}
}
