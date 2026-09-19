package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestRestoreQueuesALegacyClaimedSteerOnce: a store written before the
// accepted-until-recorded series can hold ExecutionState "claimed" for a steer
// (the pre-series popSteeringHead wrote it). Such a steer whose turn the
// transcript does not hold is a steer that never landed: restore normalizes it
// to accepted and queues it exactly once, rather than leaving it neither
// queued nor finalized.
func TestRestoreQueuesALegacyClaimedSteerOnce(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-legacy-claimed",
		Input:            []appwire.InputItem{{Type: "text", Text: "claimed by the old code"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	// The pre-series claimed-mark, as the old popSteeringHead wrote it.
	if err := crashed.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending := snapshot.PendingExecutions["steer-legacy-claimed"]
		pending.ExecutionState = "claimed"
		snapshot.PendingExecutions["steer-legacy-claimed"] = pending
		record := snapshot.Journal["steer-legacy-claimed"]
		record.ExecutionState = "claimed"
		snapshot.Journal["steer-legacy-claimed"] = record
		return nil
	}); err != nil {
		t.Fatalf("write the legacy shape: %v", err)
	}
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions["steer-legacy-claimed"]
	if !ok || pending.ExecutionState != "accepted" || snapshot.Journal["steer-legacy-claimed"].ExecutionState != "accepted" {
		t.Fatalf("the legacy claimed steer reads pending=%v state=%q journal=%q after restore, want accepted in both", ok, pending.ExecutionState, snapshot.Journal["steer-legacy-claimed"].ExecutionState)
	}
	restored.mu.Lock()
	queued := 0
	for _, entry := range restored.steeringQueue {
		if entry.ClientMutationID == "steer-legacy-claimed" {
			queued++
		}
	}
	restored.mu.Unlock()
	if queued != 1 {
		t.Fatalf("the legacy claimed steer is queued %d time(s) after restore, want once", queued)
	}
	if carrier, _ := restored.claimSteeringCarrierInput(); !carrier.SteeringCarrier {
		t.Fatal("the restored steer cannot claim a carrier: it will never reach the model")
	}
}
